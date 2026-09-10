package fake

import (
	"context"

	"gameagent/runtime/internal/model"
)

var _ model.TextGenerator = Provider{}

func (Provider) GenerateText(ctx context.Context, req model.TextRequest) (model.TextResponse, error) {
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	req, err := model.ValidateTextRequest(req)
	if err != nil {
		return model.TextResponse{}, err
	}
	// Echo complete evidence so the fake preserves attribution and negation.
	resp := model.TextResponse{Text: req.Input}
	if err := model.ValidateTextResponse(req, resp); err != nil {
		return model.TextResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	return resp, nil
}
