package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gameagent/runtime/internal/model"
)

const textSuccessBody = `{"status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Alice did not act.","annotations":[]}]}]}`

func TestGenerateTextUsesConfiguredResponsesProviderWithoutTools(t *testing.T) {
	for _, outputLimit := range []int{0, 32} {
		t.Run(strconv.Itoa(outputLimit), func(t *testing.T) {
			req := model.TextRequest{System: " Keep claims as claims.\n", Input: "Alice did not act.", MaxOutputTokens: outputLimit}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/configured/responses" || r.Method != http.MethodPost {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer test-only-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("configured authorization or content type missing")
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				limit := outputLimit
				if limit == 0 {
					limit = 2048
				}
				want := map[string]any{
					"model": "configured-model", "instructions": req.System,
					"input":             []any{map[string]any{"role": "user", "content": req.Input}},
					"max_output_tokens": float64(limit), "stream": false, "truncation": "disabled",
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("text payload = %#v, want %#v", got, want)
				}
				_, _ = io.WriteString(w, textSuccessBody)
			}))
			defer server.Close()
			provider := NewProvider("test-only-key", "configured-model", WithBaseURL(server.URL+"/configured/"))
			provider.client = server.Client()
			var generator model.TextGenerator = provider
			resp, err := generator.GenerateText(context.Background(), req)
			if err != nil || resp.Text != "Alice did not act." {
				t.Fatalf("GenerateText = %+v, %v", resp, err)
			}
		})
	}
}

func TestGenerateTextConcatenatesActualOutputText(t *testing.T) {
	provider := newTextTestProvider(t, http.StatusOK, `{
		"status":"completed", "output_text":"not an HTTP output message",
		"output":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"private reasoning"}]},
			{"type":"message","status":"completed","role":"assistant","content":[
				{"type":"output_text","text":" Alice did ","annotations":[]},
				{"type":"output_text","text":"not act.\n","annotations":[]}
			]},
			{"type":"message","status":"completed","role":"assistant","content":[
				{"type":"output_text","text":"Bob reports completion. ","annotations":[]}
			]}
		]
	}`)
	resp, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts"})
	if err != nil || resp.Text != " Alice did not act.\nBob reports completion. " {
		t.Fatalf("GenerateText = %+v, %v", resp, err)
	}
}

func TestGenerateTextRejectsInvalidRequestsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, textSuccessBody)
	}))
	defer server.Close()
	provider := NewProvider("test-only-key", "configured-model", WithBaseURL(server.URL))
	for _, req := range []model.TextRequest{
		{},
		{Input: "facts", MaxInputTokens: 1},
		{System: strings.Repeat("!", 256), Input: "facts", MaxInputTokens: 256},
		{Input: strings.Repeat("a", 32768*4)},
		{Input: "facts", MaxInputTokens: -1},
		{Input: "facts", MaxOutputTokens: -1},
		{Input: "facts", MaxResponseBytes: -1},
	} {
		resp, err := provider.GenerateText(context.Background(), req)
		if err == nil || resp.Text != "" {
			t.Fatalf("invalid request returned %+v, %v", resp, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.GenerateText(ctx, model.TextRequest{Input: "facts"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
	provider.apiKey = ""
	if _, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts"}); err == nil {
		t.Fatal("missing key was accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests made %d HTTP calls", calls.Load())
	}
}

func TestGenerateTextRejectsNonTextAndIncompleteResponses(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing output", `{"status":"completed"}`},
		{"empty output", `{"status":"completed","output":[]}`},
		{"null", `null`},
		{"shortcut only", `{"status":"completed","output_text":"not an output message"}`},
		{"missing status", strings.Replace(textSuccessBody, `"status":"completed",`, "", 1)},
		{"incomplete", strings.Replace(textSuccessBody, `"status":"completed"`, `"status":"incomplete"`, 1)},
		{"in progress", strings.Replace(textSuccessBody, `"status":"completed"`, `"status":"in_progress"`, 1)},
		{"failed", strings.Replace(textSuccessBody, `"status":"completed"`, `"status":"failed"`, 1)},
		{"canceled", strings.Replace(textSuccessBody, `"status":"completed"`, `"status":"cancelled"`, 1)},
		{"queued", strings.Replace(textSuccessBody, `"status":"completed"`, `"status":"queued"`, 1)},
		{"incomplete details", strings.Replace(textSuccessBody, `"incomplete_details":null`, `"incomplete_details":{"reason":"max_output_tokens"}`, 1)},
		{"item incomplete", strings.ReplaceAll(textSuccessBody, `"type":"message","status":"completed"`, `"type":"message","status":"incomplete"`)},
		{"missing item status", strings.ReplaceAll(textSuccessBody, `"type":"message","status":"completed"`, `"type":"message"`)},
		{"wrong role", strings.Replace(textSuccessBody, `"role":"assistant"`, `"role":"user"`, 1)},
		{"empty text", strings.Replace(textSuccessBody, "Alice did not act.", "", 1)},
		{"mixed missing text", strings.Replace(textSuccessBody, `"content":[`, `"content":[{"type":"output_text"},`, 1)},
		{"mixed null text", strings.Replace(textSuccessBody, `"content":[`, `"content":[{"type":"output_text","text":null},`, 1)},
		{"mixed empty text", strings.Replace(textSuccessBody, `"content":[`, `"content":[{"type":"output_text","text":""},`, 1)},
		{"whitespace", strings.Replace(textSuccessBody, "Alice did not act.", ` \n\t`, 1)},
		{"refusal", `{"status":"completed","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"refusal","refusal":"secret-body"}]}]}`},
		{"mixed refusal", strings.Replace(textSuccessBody, `"content":[`, `"content":[{"type":"refusal","refusal":"secret-body"},`, 1)},
		{"reasoning only", `{"status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"not the answer"}]}]}`},
		{"tool only", `{"status":"completed","output":[{"type":"function_call","name":"__gameagent_settle","arguments":"{}"}]}`},
		{"mixed tool", strings.Replace(textSuccessBody, `"output":[`, `"output":[{"type":"function_call","name":"act","arguments":"{}"},`, 1)},
		{"unknown content", strings.Replace(textSuccessBody, `"type":"output_text"`, `"type":"input_text"`, 1)},
		{"api error", strings.Replace(textSuccessBody, `"error":null`, `"error":{"code":"secret-code","message":"secret-body"}`, 1)},
		{"wrong content type", strings.Replace(textSuccessBody, `"text":"Alice did not act."`, `"text":42`, 1)},
		{"malformed", `{"secret-body":`},
		{"trailing json", textSuccessBody + `{}`},
		{"invalid utf8", strings.Replace(textSuccessBody, "Alice", string([]byte{0xff}), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newTextTestProvider(t, http.StatusOK, tc.body)
			resp, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts"})
			if err == nil || resp.Text != "" {
				t.Fatalf("response = %+v, %v; want failure without text", resp, err)
			}
			if strings.Contains(err.Error(), "secret-") || strings.Contains(err.Error(), "\n") {
				t.Fatalf("response error leaked body: %q", err)
			}
		})
	}
}

func TestGenerateTextEnforcesOutputAndResponseBounds(t *testing.T) {
	large := strings.TrimSuffix(textSuccessBody, "}") + `,"padding":"` + strings.Repeat("a", 1<<20) + `"}`
	for _, tc := range []struct {
		name string
		body string
		req  model.TextRequest
		want error
	}{
		{"output tokens", textSuccessBody, model.TextRequest{MaxOutputTokens: 1}, model.ErrTextOutputTooLarge},
		{"unicode output", strings.Replace(textSuccessBody, "Alice did not act.", "\u672a\u5b8c\u6210", 1), model.TextRequest{MaxOutputTokens: 2}, model.ErrTextOutputTooLarge},
		{"default output limit", strings.Replace(textSuccessBody, "Alice did not act.", strings.Repeat("a", 2048*4+1), 1), model.TextRequest{}, model.ErrTextOutputTooLarge},
		{"exact response bytes", textSuccessBody, model.TextRequest{MaxResponseBytes: len(textSuccessBody)}, nil},
		{"response overflow", textSuccessBody, model.TextRequest{MaxResponseBytes: len(textSuccessBody) - 1}, model.ErrTextResponseTooLarge},
		{"default response limit", large, model.TextRequest{}, model.ErrTextResponseTooLarge},
		{"custom response limit", large, model.TextRequest{MaxResponseBytes: len(large)}, nil},
		{"max int byte limit", textSuccessBody, model.TextRequest{MaxResponseBytes: int(^uint(0) >> 1)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newTextTestProvider(t, http.StatusOK, tc.body)
			tc.req.Input = "facts"
			resp, err := provider.GenerateText(context.Background(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.want != nil && resp.Text != "" {
				t.Fatalf("failed response returned partial text: %q", resp.Text)
			}
			if tc.want == nil && resp.Text != "Alice did not act." {
				t.Fatalf("text = %q", resp.Text)
			}
		})
	}
}

func TestGenerateTextRejectsUnpairedSurrogateEscapes(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"high", `"before\ud800after"`},
		{"low", `"before\udc00after"`},
		{"high at end", `"\uDBFF"`},
		{"low at end", `"\uDFFF"`},
		{"high followed by BMP", `"\ud800\u0041"`},
		{"two high", `"\ud800\udbff"`},
		{"reversed pair", `"\udc00\ud800"`},
		{"separated pair", `"\ud800x\udc00"`},
		{"escaped low", `"\ud800\\udc00"`},
		{"valid pair then low", `"\ud834\udd1e\udc00"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(textSuccessBody, `"Alice did not act."`, tc.text, 1)
			provider := newTextTestProvider(t, http.StatusOK, body)
			resp, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts"})
			if !errors.Is(err, model.ErrInvalidTextResponse) || resp.Text != "" {
				t.Fatalf("GenerateText = %+v, %v; want invalid response without text", resp, err)
			}
		})
	}
}

func TestGenerateTextPreservesValidUnicodeAndJSONEscapes(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"pair", `"\uD834\uDd1e"`, "\U0001D11E"},
		{"pair boundaries", `"\ud800\udc00\udbff\udfff"`, "\U00010000\U0010FFFF"},
		{"actual replacement", "\"\uFFFD\"", "\uFFFD"},
		{"escaped replacement", `"\ufffd"`, "\uFFFD"},
		{"escaped backslash", `"\\ud800"`, `\ud800`},
		{"encoded backslash", `"\u005cud800"`, `\ud800`},
		{"backslash and pair", `"\\\ud834\udd1e"`, "\\\U0001D11E"},
		{"escaped quote", `"a\"b\ud834\udd1e"`, "a\"b\U0001D11E"},
		{"large number text", `"9007199254740993"`, "9007199254740993"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(textSuccessBody, `"Alice did not act."`, tc.text, 1)
			provider := newTextTestProvider(t, http.StatusOK, body)
			resp, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts"})
			if err != nil || resp.Text != tc.want {
				t.Fatalf("GenerateText = %+v, %v; want %q", resp, err, tc.want)
			}
		})
	}
}

func TestGenerateTextHTTPErrorIsStatusOnly(t *testing.T) {
	provider := newTextTestProvider(t, http.StatusBadRequest, "secret-body\n"+strings.Repeat("a", 1<<20))
	resp, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts"})
	if err == nil || resp.Text != "" || !strings.Contains(err.Error(), "status=400") || strings.Contains(err.Error(), "secret-body") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("HTTP error = %+v, %v", resp, err)
	}
}

func TestGenerateTextRejectsTruncatedHTTPBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(textSuccessBody)+1))
		_, _ = io.WriteString(w, textSuccessBody)
	}))
	defer server.Close()
	provider := NewProvider("test-only-key", "configured-model", WithBaseURL(server.URL))
	resp, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts"})
	if err == nil || resp.Text != "" {
		t.Fatalf("truncated body returned %+v, %v", resp, err)
	}
}

func TestGenerateTextRespectsDeadlineDuringHTTPAndBodyRead(t *testing.T) {
	for _, flushHeaders := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[flushHeaders], func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if flushHeaders {
					_, _ = io.WriteString(w, `{"output":[`)
					w.(http.Flusher).Flush()
				}
				<-release
			}))
			defer server.Close()
			defer close(release)
			provider := NewProvider("test-only-key", "configured-model", WithBaseURL(server.URL))
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			resp, err := provider.GenerateText(ctx, model.TextRequest{Input: "facts"})
			if !errors.Is(err, context.DeadlineExceeded) || resp.Text != "" {
				t.Fatalf("deadline response = %+v, %v", resp, err)
			}
		})
	}
}

func TestGenerateTextBoundsActualReadAndClosesBody(t *testing.T) {
	body := &textCountingBody{Reader: strings.NewReader(strings.Repeat("x", 4096))}
	provider := NewProvider("test-only-key", "configured-model")
	provider.client = &http.Client{Transport: textRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}
	_, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts", MaxResponseBytes: 128})
	if !errors.Is(err, model.ErrTextResponseTooLarge) || body.read > 129 || !body.closed {
		t.Fatalf("read = %d, closed = %v, error = %v", body.read, body.closed, err)
	}
}

func TestGenerateTextRedactsTransportErrors(t *testing.T) {
	provider := NewProvider("test-only-key", "configured-model", WithBaseURL("http://invalid.test/secret-path"))
	provider.client = &http.Client{Transport: textRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("secret-key\nsecret-body")
	})}
	resp, err := provider.GenerateText(context.Background(), model.TextRequest{Input: "facts"})
	if err == nil || resp.Text != "" || strings.Contains(err.Error(), "secret-") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("transport error = %+v, %v", resp, err)
	}
}

func newTextTestProvider(t *testing.T, status int, body string) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return NewProvider("test-only-key", "configured-model", WithBaseURL(server.URL))
}

type textRoundTripper func(*http.Request) (*http.Response, error)

func (f textRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type textCountingBody struct {
	*strings.Reader
	read   int
	closed bool
}

func (b *textCountingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *textCountingBody) Close() error {
	b.closed = true
	return nil
}
