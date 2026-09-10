package model

import "fmt"

type WindowLimits struct {
	ContextTokens int `json:"context_window_tokens"`
	OutputTokens  int `json:"max_output_tokens"`
}

type WindowProvider interface{ ModelWindow() WindowLimits }

func (w WindowLimits) Validate() error {
	if w == (WindowLimits{}) {
		return nil
	}
	if w.ContextTokens <= 0 || w.OutputTokens <= 0 || w.OutputTokens >= w.ContextTokens {
		return fmt.Errorf("model context_window_tokens must exceed positive max_output_tokens")
	}
	return nil
}
func (w WindowLimits) InputTokens() int { return max(0, w.ContextTokens-w.OutputTokens) }
func (w WindowLimits) Check(input, output int) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if w.ContextTokens == 0 {
		return fmt.Errorf("model context window is not configured")
	}
	if input < 0 || output <= 0 || output > w.OutputTokens || input > w.ContextTokens-output {
		return fmt.Errorf("request exceeds configured model window")
	}
	return nil
}
