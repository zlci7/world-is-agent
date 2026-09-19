package llm

import "gameagent/runtime/internal/model"

// DefaultWindowLimits returns the window to write when the user did not choose
// one, and the zero value when there is nothing to write.
//
// A configuration with no window is valid: the provider skips its own window
// check and the Runtime turns off summarisation, because neither can be done
// without knowing how large the model's context is. That is a worse default than
// the numbers this repository already ships and runs on, so they are used here.
//
// Only combinations this repository actually states a window for appear. Guessing
// a context size for a model nobody measured would be inventing a number and
// presenting it as configuration.
func DefaultWindowLimits(provider, name string) model.WindowLimits {
	_ = name
	switch provider {
	case "deepseek":
		return model.WindowLimits{ContextTokens: 131072, OutputTokens: 12800}
	default:
		return model.WindowLimits{}
	}
}
