package llm

import (
	"strings"
	"testing"
)

// The first-run form offers whatever this reports, so an entry the Runtime cannot
// build is a form that fails after the user has typed a credential. This is the
// property that makes one list safe to serve instead of two to keep in step.
func TestEveryOfferedProviderIsConstructibleWithItsDefaultModel(t *testing.T) {
	choices := ProviderChoices()
	if len(choices) == 0 {
		t.Fatal("no provider is offered, so the first-run form has nothing to submit")
	}

	for _, choice := range choices {
		provider, err := buildProbeProvider(ProbeCandidate{Provider: choice.Provider, APIKey: "sk-test"})
		if err != nil {
			t.Errorf("offered provider %q is not constructible: %v", choice.Provider, err)
			continue
		}
		if provider == nil {
			t.Errorf("offered provider %q built a nil provider", choice.Provider)
		}
		if strings.TrimSpace(choice.Model) == "" {
			t.Errorf("offered provider %q has no default model", choice.Provider)
		}
		if got := defaultProbeModel(choice.Provider); got != choice.Model {
			t.Errorf("provider %q offers model %q but falls back to %q", choice.Provider, choice.Model, got)
		}
	}
}
