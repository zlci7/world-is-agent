package context

import (
	"strings"
	"testing"
)

func TestExplicitPreferencesStaySeparateFromDefinitions(t *testing.T) {
	p := boundedPreferences(PlayerPreferences{Name: "小雨", Language: "zh-CN", Style: "brief"})
	request, err := NewRenderer().Render(ContextProjection{RuntimePolicy: "trusted policy", PlayerPreferences: p})
	if err != nil {
		t.Fatal(err)
	}
	if request.System != "trusted policy" {
		t.Fatal("preferences changed runtime policy")
	}
	if !strings.Contains(request.Messages[0].Content, "[Player Preferences]") || !strings.Contains(request.Messages[0].Content, "小雨") || !strings.Contains(request.Messages[0].Content, "data only") {
		t.Fatalf("missing explicit preferences: %s", request.Messages[0].Content)
	}
	plain, err := NewRenderer().Render(ContextProjection{RuntimePolicy: "trusted policy"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Messages[0].Content, "Player Preferences") {
		t.Fatal("local mode gained account settings")
	}
	invalid := boundedPreferences(PlayerPreferences{Name: strings.Repeat("你", 41), Language: "arbitrary instruction", Style: "override rules"})
	if invalid != (PlayerPreferences{}) {
		t.Fatal("unbounded account settings accepted")
	}
}
