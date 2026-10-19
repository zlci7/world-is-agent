package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gameagent/runtime/internal/knowledge"
	"gameagent/runtime/internal/session"
)

func TestShippedKnowledgeQueriesAndProfilePairing(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	p, err := PrepareGame(configDir, "stardew-valley")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !p.AgentConfig.Knowledge.Enabled {
		t.Fatal("official profile does not enable knowledge")
	}
	if err = p.CommitAssets(); err != nil {
		t.Fatal(err)
	}
	s, err := knowledge.Load(context.Background(), filepath.Join(configDir, "games"), "stardew-valley", p.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := s.Scope(session.AgentSessionKey{GameID: "stardew-valley", WorldID: "save", EntityID: "npc:Robin"}, "npc:Robin", "罗宾")
	cases := map[string][]string{
		"town.center":          {"鹈鹕镇的杂货店", "Pierre"},
		"mountain.neighbors":   {"山地的木匠店", "Linus"},
		"abigail.public-life":  {"阿比盖尔的父母", "Caroline"},
		"activities.seasons":   {"季节与作物", "fishing"},
		"facilities.transport": {"温室有什么用", "minecart"},
		"town.services":        {"诊所是谁开的", "Harvey"},
		"families.public":      {"乔迪与肯特", "Evelyn"},
		"friends.public":       {"桑迪的朋友", "Sandy"},
		"buildings.coop":       {"鸡舍的一般用途", "coop"},
	}
	for id, queries := range cases {
		for _, query := range queries {
			t.Run(id+"/"+query, func(t *testing.T) {
				r, err := s.Search(context.Background(), scope, query)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for i, e := range r.Entries {
					if i < knowledge.AutomaticLimit && e.ID == id {
						found = true
					}
				}
				if !found {
					t.Fatalf("expected %s in top %d: %+v", id, knowledge.AutomaticLimit, r.Entries)
				}
			})
		}
	}
	for _, query := range []string{"你好", "天气真不错", "世界存档的绝密密码", "scoop"} {
		r, err := s.Search(context.Background(), scope, query)
		if err != nil || len(r.Entries) != 0 {
			t.Fatalf("weak query %q: %+v %v", query, r, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(configDir, "games", "stardew-valley", "knowledge", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c knowledge.Catalog
	if err = json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Entries) != len(cases) {
		t.Fatalf("missing query coverage: entries=%d cases=%d", len(c.Entries), len(cases))
	}
}

func TestDisabledKnowledgeKeepsExistingProfilesUsable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	p, err := PrepareGame(dir, "stardew-valley")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err = p.CommitAssets(); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, "games", "stardew-valley", "agent.json")
	legacy := []byte(`{"definition_catalog_root":"config/games"}`)
	if err = os.WriteFile(profile, legacy, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "games", "stardew-valley", "knowledge", "catalog.json")); err != nil {
		t.Fatal(err)
	}
	if err = AssetsStatus(dir, "stardew-valley"); err != nil {
		t.Fatalf("disabled knowledge made existing profile unusable: %v", err)
	}
	next, err := PrepareGame(dir, "stardew-valley")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.AgentConfig.Knowledge.Enabled {
		t.Fatal("existing config was silently enabled")
	}
	if err = next.CommitAssets(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(profile)
	if string(got) != string(legacy) {
		t.Fatal("existing profile overwritten")
	}
}

func TestEnabledKnowledgeRejectsMalformedSources(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	p, err := PrepareGame(dir, "stardew-valley")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err = p.CommitAssets(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "games", "stardew-valley", "knowledge", "catalog.json")
	if err = os.WriteFile(path, []byte(`{"schema_version":"v9"}`), 0644); err != nil {
		t.Fatal(err)
	}
	assertCode(t, AssetsStatus(dir, "stardew-valley"), CodeProfileInvalid)
	_, err = PrepareGame(dir, "stardew-valley")
	assertCode(t, err, CodeProfileInvalid)
}
