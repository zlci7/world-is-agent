package knowledge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/session"
)

func fixture(t *testing.T, game string) (Catalog, definition.Catalog) {
	t.Helper()
	defs, err := definition.NewCatalog(nil, []definition.AgentDefinition{{SchemaVersion: "v1alpha1", GameID: game, DefinitionID: "builder"}, {SchemaVersion: "v1alpha1", GameID: game, DefinitionID: "fisher"}})
	if err != nil {
		t.Fatal(err)
	}
	return Catalog{SchemaVersion: "v1", GameID: game, SourceVersion: "test-v1", Entries: []Entry{
		{ID: "coop", Title: "鸡舍建设", Aliases: []string{"鸡舍", "coop"}, Visibility: Visibility{Kind: "public"}, Text: "鸡舍用来饲养动物。当前是否建好以观察为准。"},
		{ID: "fishing", Title: "钓鱼条件", Aliases: []string{"钓鱼", "fishing"}, Visibility: Visibility{Kind: "public"}, Text: "钓鱼条件取决于地点、季节和天气。"},
		{ID: "private", Title: "木匠旧信", Aliases: []string{"旧信"}, Visibility: Visibility{Kind: "definitions", DefinitionIDs: []string{"builder"}}, Text: "木匠私有的旧信背景。"},
	}}, defs
}

func openFixture(t *testing.T, game string) *Service {
	t.Helper()
	c, defs := fixture(t, game)
	data, _ := json.Marshal(c)
	s, err := Open(context.Background(), data, game, defs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestSearchScopeAliasesAndIndependentInstances(t *testing.T) {
	a, b := openFixture(t, "forest"), openFixture(t, "station")
	for _, s := range []*Service{a, b} {
		owner := session.AgentSessionKey{GameID: s.gameID, WorldID: "save", EntityID: "npc"}
		for _, tc := range []struct{ query, definition, want string }{
			{"鸡舍怎么建设", "builder", "coop"}, {"COOP", "fisher", "coop"}, {"coop怎么用", "fisher", "coop"}, {"scoop", "builder", ""},
			{"你好", "builder", ""}, {"旧信", "builder", "private"}, {"旧信", "fisher", ""}, {"旧信", "unknown", ""},
			{"   ", "builder", ""},
			{"旧信", " builder ", "private"},
			{"怎么钓鱼", "fisher", "fishing"},
		} {
			t.Run(s.gameID+"/"+tc.query+"/"+tc.definition, func(t *testing.T) {
				r, err := s.Search(context.Background(), s.Scope(owner, tc.definition, "角色"), tc.query)
				if err != nil {
					t.Fatal(err)
				}
				if tc.want == "" {
					if r.Status != "no_match" || len(r.Entries) != 0 {
						t.Fatalf("unexpected result: %+v", r)
					}
					return
				}
				if r.Status != "ok" || len(r.Entries) == 0 || r.Entries[0].ID != tc.want {
					t.Fatalf("result: %+v", r)
				}
			})
		}
		scope := s.Scope(owner, "builder", "角色")
		scope.Owner.GameID = "other"
		if _, err := s.Search(context.Background(), scope, "鸡舍"); err == nil {
			t.Fatal("foreign game accepted")
		}
		scope = s.Scope(owner, "builder", "角色")
		scope.ContentHash = "old"
		if _, err := s.Search(context.Background(), scope, "鸡舍"); err == nil {
			t.Fatal("foreign snapshot accepted")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.Search(ctx, s.Scope(owner, "builder", "角色"), "鸡舍"); err == nil {
			t.Fatal("cancel ignored")
		}
		if _, err := s.Search(context.Background(), s.Scope(owner, "builder", "角色"), strings.Repeat("鸡", 257)); err == nil {
			t.Fatal("oversize query accepted")
		}
	}
}

func TestCatalogValidation(t *testing.T) {
	for _, name := range []string{"version", "game", "duplicate", "visibility", "unknown definition", "missing definition", "public restriction", "long body", "empty alias", "no entries"} {
		t.Run(name, func(t *testing.T) {
			c, defs := fixture(t, "forest")
			switch name {
			case "version":
				c.SchemaVersion = "v2"
			case "game":
				c.GameID = "other"
			case "duplicate":
				c.Entries = append(c.Entries, c.Entries[0])
			case "visibility":
				c.Entries[0].Visibility.Kind = "secret"
			case "unknown definition":
				c.Entries[2].Visibility.DefinitionIDs = []string{"missing"}
			case "missing definition":
				c.Entries[2].Visibility.DefinitionIDs = nil
			case "public restriction":
				c.Entries[0].Visibility.DefinitionIDs = []string{"builder"}
			case "long body":
				c.Entries[0].Text = strings.Repeat("字", 513)
			case "empty alias":
				c.Entries[0].Aliases = []string{" "}
			case "no entries":
				c.Entries = nil
			}
			data, _ := json.Marshal(c)
			if _, err := Open(context.Background(), data, "forest", defs); err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
	c, defs := fixture(t, "forest")
	data, _ := json.Marshal(c)
	for _, bad := range [][]byte{append(data, []byte(" {}")...), []byte(`{"schema_version":"v1","unknown":true}`), make([]byte, MaxFileBytes+1)} {
		if _, err := Parse(bad, "forest", defs); err == nil {
			t.Fatal("invalid document accepted")
		}
	}
}

func TestKnowledgeConcurrentReadScopes(t *testing.T) {
	s := openFixture(t, "forest")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "builder"
			if i%2 == 1 {
				id = "fisher"
			}
			scope := s.Scope(session.AgentSessionKey{GameID: "forest", WorldID: "save", EntityID: id}, id, "角色")
			r, err := s.Search(context.Background(), scope, "旧信")
			if err != nil {
				t.Error(err)
				return
			}
			if (id == "builder" && len(r.Entries) != 1) || (id == "fisher" && len(r.Entries) != 0) {
				t.Errorf("scope %s: %+v", id, r)
			}
		}(i)
	}
	wg.Wait()
}
