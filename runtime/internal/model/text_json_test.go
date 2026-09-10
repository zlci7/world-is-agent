package model

import (
	"bytes"
	"errors"
	"testing"
)

func TestValidateTextJSONRejectsMalformedJSONAndUnicode(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"empty", ""},
		{"whitespace", " \r\n\t"},
		{"unterminated string", `"text`},
		{"unterminated escape", `"text\`},
		{"unknown escape", `"\x00"`},
		{"invalid hex", `"\uZZZZ"`},
		{"short unicode escape", `"\u12"`},
		{"literal newline", "\"a\nb\""},
		{"trailing value", `{} []`},
		{"trailing comma", `{"text":"ok",}`},
		{"escape outside string", `\u0061`},
		{"invalid utf8", string([]byte{'"', 0xff, '"'})},
		{"raw utf8 surrogate", string([]byte{'"', 0xed, 0xa0, 0x80, '"'})},
		{"high", `"before\ud800after"`},
		{"low", `"before\udc00after"`},
		{"high at end", `"\uDBFF"`},
		{"low at end", `"\uDFFF"`},
		{"high followed by BMP", `"\ud800\u0041"`},
		{"two high", `"\ud800\udbff"`},
		{"two low", `"\udc00\udfff"`},
		{"reversed pair", `"\udc00\ud800"`},
		{"separated pair", `"\ud800x\udc00"`},
		{"escaped low", `"\ud800\\udc00"`},
		{"encoded backslash before low", `"\ud800\u005cudc00"`},
		{"valid pair then low", `"\ud834\udd1e\udc00"`},
		{"pair across strings", `["\ud800","\udc00"]`},
		{"object key", `{"\ud800":"ok"}`},
		{"nested metadata", `{"text":"ok","metadata":["\udc00"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.input)
			before := bytes.Clone(data)
			if err := ValidateTextJSON(data); !errors.Is(err, ErrInvalidTextResponse) {
				t.Fatalf("ValidateTextJSON = %v, want invalid response", err)
			}
			if !bytes.Equal(data, before) {
				t.Fatal("validation changed the input")
			}
		})
	}
}

func TestValidateTextJSONPreservesValidJSON(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"empty string", `""`},
		{"null", `null`},
		{"true", `true`},
		{"false", `false`},
		{"large integer", `9007199254740993`},
		{"number exponent", `-123.456e+78`},
		{"object and array", `{"text":"ok","metadata":[null,true,false,9007199254740993,{}]}`},
		{"whitespace", " \t\r\n[\"text\"] \n"},
		{"ordinary escapes", `"\"\\\/\b\f\n\r\t"`},
		{"BMP boundaries", `"\u0000\uD7ff\uE000\uFFFF"`},
		{"pair", `"\uD834\uDd1e"`},
		{"pair boundaries", `"\ud800\udc00\udbff\udfff"`},
		{"actual supplementary rune", "\"\U0001D11E\""},
		{"actual replacement", "\"\uFFFD\""},
		{"escaped replacement", `"\ufffd"`},
		{"escaped backslash", `"\\ud800"`},
		{"encoded backslash", `"\u005cud800"`},
		{"backslash and pair", `"\\\ud834\udd1e"`},
		{"escaped quote", `"a\"b\ud834\udd1e"`},
		{"object key pair", `{"\ud834\udd1e":"ok"}`},
		{"object key literal escape", `{"\\ud800":"ok"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.input)
			before := bytes.Clone(data)
			if err := ValidateTextJSON(data); err != nil {
				t.Fatalf("ValidateTextJSON = %v", err)
			}
			if !bytes.Equal(data, before) {
				t.Fatal("validation changed the input")
			}
		})
	}
}
