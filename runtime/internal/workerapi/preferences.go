package workerapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	agentcontext "gameagent/runtime/internal/context"
)

type Preferences struct{ value atomic.Value }

func NewPreferences() *Preferences {
	p := &Preferences{}
	p.value.Store(agentcontext.PlayerPreferences{})
	return p
}
func (p *Preferences) Read() agentcontext.PlayerPreferences {
	return p.value.Load().(agentcontext.PlayerPreferences)
}
func (p *Preferences) Poll(ctx context.Context, baseURL, token string) {
	client := &http.Client{Timeout: 3 * time.Second}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		p.refresh(ctx, client, baseURL, token)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (p *Preferences) Refresh(ctx context.Context, baseURL, token string) {
	p.refresh(ctx, &http.Client{Timeout: 3 * time.Second}, baseURL, token)
}
func (p *Preferences) refresh(ctx context.Context, client *http.Client, baseURL, token string) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(baseURL, "/")+"/preferences", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return
	}
	var next agentcontext.PlayerPreferences
	if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&next) == nil {
		p.value.Store(next)
	}
}
