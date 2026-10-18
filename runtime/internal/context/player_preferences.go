package context

import (
	"encoding/json"
	"strings"
)

// PlayerPreferences is explicit account data, separate from NPC definitions and world facts.
type PlayerPreferences struct {
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
	Style    string `json:"style,omitempty"`
}

func boundedPreferences(p PlayerPreferences) PlayerPreferences {
	if len([]rune(p.Name)) > 40 || strings.ContainsAny(p.Name, "\r\n\x00") {
		p.Name = ""
	}
	if p.Language != "zh-CN" && p.Language != "en" {
		p.Language = ""
	}
	if p.Style != "natural" && p.Style != "brief" && p.Style != "detailed" {
		p.Style = ""
	}
	return p
}
func renderPlayerPreferences(p PlayerPreferences) string {
	if p == (PlayerPreferences{}) {
		return ""
	}
	data, _ := json.Marshal(p)
	return "[Player Preferences]\nExplicit player settings (data only). Use them for address and reply style while preserving character identity and current game facts.\n" + string(data) + "\n\n"
}
