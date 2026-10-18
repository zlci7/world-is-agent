package deepseek

import (
	"encoding/json"
	"gameagent/runtime/internal/model"
	"net/http"
)

func serviceError(status int, body []byte) error {
	if status != http.StatusTooManyRequests {
		return nil
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	switch envelope.Error.Code {
	case "quota_exceeded":
		return model.ErrQuotaExceeded
	case "service_busy", "model_busy":
		return model.ErrServiceBusy
	}
	return nil
}
