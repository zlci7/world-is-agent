package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"gameagent/runtime/internal/llm/deepseek"
	"gameagent/runtime/internal/llm/openai"
	"gameagent/runtime/internal/model"
)

// Probe codes report why a candidate configuration could not be used. They are
// deliberately few: the first-run flow needs to tell the user which thing to fix,
// not to describe the provider's error taxonomy.
const (
	// ProbeOK means the provider answered.
	ProbeOK = "ok"
	// ProbeInvalidConfiguration means the candidate never reached the network:
	// an unknown provider, an unusable window, or a missing credential.
	ProbeInvalidConfiguration = "invalid_configuration"
	// ProbeAuthenticationFailed means the provider rejected the credential.
	ProbeAuthenticationFailed = "authentication_failed"
	// ProbeNetworkUnavailable means the provider could not be reached.
	ProbeNetworkUnavailable = "network_unavailable"
	// ProbeProviderError means the provider answered with something else.
	ProbeProviderError = "provider_error"
)

// ProbeTimeout bounds one probe. A first-run form waits on this, so it is short
// enough to keep the page responsive and long enough to survive a slow network.
const ProbeTimeout = 20 * time.Second

// ProbeCandidate is a model configuration that is not on disk yet: a credential
// the user has typed, which is checked and then either written or discarded.
//
// This is the one path where a credential is passed as a plain string. It is not a
// relaxation of the rule that a stored configuration must reference its
// credential rather than contain it: this value has no file to live in yet, and it
// is never persisted, logged or traced here.
type ProbeCandidate struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
	Window   model.WindowLimits
}

// ProbeOutcome is the result of one probe. Message explains a failure without
// carrying the credential.
type ProbeOutcome struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// ProbeConnection checks a candidate configuration by making one minimal request.
//
// Construction happens first, so a candidate that is wrong on its face is
// reported as such without a network call: an unknown provider, an unusable
// window, or a blank credential never need the provider to answer.
func ProbeConnection(ctx context.Context, candidate ProbeCandidate) ProbeOutcome {
	provider, err := buildProbeProvider(candidate)
	if err != nil {
		return ProbeOutcome{Code: ProbeInvalidConfiguration, Message: err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	// The smallest request that proves the credential works. The content is
	// irrelevant; the answer is whether the provider accepts it at all.
	_, err = provider.Generate(ctx, model.Request{
		System:   "Reply with a single word.",
		Messages: []model.Message{{Role: model.RoleUser, Content: "ok"}},
	})
	if err == nil {
		return ProbeOutcome{OK: true, Code: ProbeOK}
	}
	return classifyProbeFailure(err)
}

func buildProbeProvider(candidate ProbeCandidate) (model.Provider, error) {
	if strings.TrimSpace(candidate.APIKey) == "" {
		return nil, errors.New("an API key is required")
	}
	// The window is resolved here exactly as the setup commit resolves it, so the
	// configuration that gets probed is the configuration that gets written.
	if candidate.Window == (model.WindowLimits{}) {
		candidate.Window = DefaultWindowLimits(candidate.Provider, candidate.Model)
	}
	if err := candidate.Window.Validate(); err != nil {
		return nil, err
	}

	switch strings.TrimSpace(candidate.Provider) {
	case "openai":
		name := candidate.Model
		if name == "" {
			name = "gpt-5-mini"
		}
		return openai.NewProvider(candidate.APIKey, name, openai.WithBaseURL(candidate.BaseURL), openai.WithModelWindow(candidate.Window)), nil
	case "deepseek":
		name := candidate.Model
		if name == "" {
			name = "deepseek-v4-flash"
		}
		return deepseek.NewProvider(candidate.APIKey, name, deepseek.WithBaseURL(candidate.BaseURL), deepseek.WithModelWindow(candidate.Window)), nil
	default:
		return nil, fmt.Errorf("unsupported model provider %q", candidate.Provider)
	}
}

// classifyProbeFailure turns a provider error into one of the few codes the form
// can act on. The provider's own message is not passed through: it is written for
// a log, and these responses reach a browser.
func classifyProbeFailure(err error) ProbeOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return ProbeOutcome{Code: ProbeNetworkUnavailable, Message: "the provider did not answer in time"}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return ProbeOutcome{Code: ProbeNetworkUnavailable, Message: "the provider could not be reached"}
	}

	message := err.Error()
	switch {
	case strings.Contains(message, "401"), strings.Contains(message, "403"),
		strings.Contains(message, "unauthorized"), strings.Contains(message, "authentication"):
		return ProbeOutcome{Code: ProbeAuthenticationFailed, Message: "the provider rejected the API key"}
	case strings.Contains(message, "no such host"), strings.Contains(message, "connection refused"),
		strings.Contains(message, "dial tcp"), strings.Contains(message, "EOF"),
		strings.Contains(message, "certificate"):
		return ProbeOutcome{Code: ProbeNetworkUnavailable, Message: "the provider could not be reached"}
	default:
		return ProbeOutcome{Code: ProbeProviderError, Message: "the provider answered with an error"}
	}
}
