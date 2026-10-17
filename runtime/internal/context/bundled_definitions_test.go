package context_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/session"
)

func TestBundledStardewDefinitionsFitBudgetWithIndependentWorldObservations(t *testing.T) {
	catalog, err := definition.LoadGameCatalogFromDir(filepath.Join("..", "..", "config", "games"), "stardew-valley")
	if err != nil {
		t.Fatal(err)
	}
	game, ok := catalog.FindGame("stardew-valley")
	if !ok {
		t.Fatal("game definition is missing")
	}
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{MaxDefinitionTokens: 4096})

	for _, name := range strings.Fields("Abigail Alex Caroline Clint Demetrius Dwarf Elliott Emily Evelyn George Gus Haley Harvey Jas Jodi Kent Krobus Leah Leo Lewis Linus Marnie Maru Pam Penny Pierre Robin Sam Sandy Sebastian Shane Vincent Willy Wizard") {
		definitionID := "npc:" + name
		t.Run(definitionID, func(t *testing.T) {
			npc, ok := catalog.FindAgent(game.GameID, definitionID)
			if !ok {
				t.Fatal("NPC definition is missing")
			}
			for _, scenario := range []struct {
				worldID string
				bond    int
				otherID string
			}{
				{worldID: "world-a", bond: 0, otherID: "world-b"},
				{worldID: "world-b", bond: 8, otherID: "world-a"},
			} {
				t.Run(scenario.worldID, func(t *testing.T) {
					key := session.AgentSessionKey{GameID: game.GameID, WorldID: scenario.worldID, EntityID: definitionID}
					target := &protocolv1alpha2.EntityRef{EntityId: key.EntityID, DefinitionId: definitionID}
					result, err := engine.Build(agentcontext.BuildInput{
						SessionKey:      key,
						CanonicalTarget: target,
						AgentDescriptor: definition.NewAgentInstanceDescriptor(key, target),
						GameDefinition:  &game,
						AgentDefinition: &npc,
						RuntimePolicy:   "Stay in character and use the current observation.",
						Event: &protocolv1alpha2.GameEvent{
							EventId: "event-1", WorldId: key.WorldID, TargetEntityId: key.EntityID,
						},
						Observation: &protocolv1alpha2.Observation{
							WorldId: key.WorldID, EntityId: key.EntityID,
							State: mustStruct(t, map[string]any{
								"simulation": map[string]any{"bond_level": scenario.bond},
							}),
						},
					})
					if err != nil {
						t.Fatal(err)
					}
					definitionTokens := 0
					for _, section := range result.Report.Sections {
						if section.Name == "game_definition" || section.Name == "agent_definition" {
							if !section.Included || section.Cropped {
								t.Fatalf("definition was omitted or cropped: %+v", section)
							}
							definitionTokens += section.ProjectionEstimatedTokens
						}
					}
					request, err := agentcontext.NewRenderer().Render(result.Projection)
					if err != nil {
						t.Fatal(err)
					}
					content := request.Messages[0].Content
					assertContainsAll(t, content,
						npc.Identity,
						npc.BehaviorGuidelines[len(npc.BehaviorGuidelines)-1],
						game.NarrativeConstraints[len(game.NarrativeConstraints)-1],
						"world_id: "+scenario.worldID,
						fmt.Sprintf(`"bond_level": %d`, scenario.bond),
					)
					if strings.Contains(content, scenario.otherID) {
						t.Fatal("request contains another world's identity")
					}
					t.Logf("game and NPC definitions: %d estimated tokens; shared template with %s observation", definitionTokens, scenario.worldID)
				})
			}
		})
	}
}
