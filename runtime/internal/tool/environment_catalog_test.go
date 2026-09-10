package tool

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tokenestimate"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestBuildEnvironmentToolCatalogBuildsValidatedCatalog(t *testing.T) {
	catalog, diagnostics, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Revision: 7,
		Capabilities: []*protocolv1alpha2.Capability{
			nil,
			{Name: "", InputSchemaJson: `{"type":"object"}`},
			{Name: " padded ", InputSchemaJson: `{"type":"object"}`},
			{Name: "broken_schema", InputSchemaJson: `{`},
			{Name: "array_schema", InputSchemaJson: `[]`},
			{Name: "null_schema", InputSchemaJson: `null`},
			{Name: "scalar_schema", InputSchemaJson: `"text"`},
			{Name: "typed_array", InputSchemaJson: `{"type":"array"}`},
			{Name: "bad_policy", InputSchemaJson: `{"type":"object"}`, Extensions: invalidToolPolicyExtensions(t)},
			{Name: "duplicate", InputSchemaJson: `{"type":"object"}`},
			{Name: "duplicate", InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"}}}`},
			{
				Name:            "move_to",
				Description:     "Move the NPC.",
				InputSchemaJson: `{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`,
				ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_ASYNC,
				ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_PARALLEL_SAFE,
				Extensions:      toolPolicyExtensions(t, true, true),
			},
			{
				Name:            "emote",
				Description:     "Display an emote.",
				InputSchemaJson: `{"properties":{"emote":{"type":"string"}}}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}
	if catalog == nil {
		t.Fatal("catalog is nil")
	}

	available := catalog.Available()
	if got, want := toolNames(available), []string{"emote", "move_to"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Available names = %v, want %v", got, want)
	}

	moveTo, ok := catalog.Lookup("move_to")
	if !ok {
		t.Fatal("Lookup(move_to) = false, want true")
	}
	if moveTo.Kind != KindEnvironment {
		t.Fatalf("move_to Kind = %q, want environment", moveTo.Kind)
	}
	if moveTo.Execution != ExecutionAsync {
		t.Fatalf("move_to Execution = %q, want async", moveTo.Execution)
	}
	if moveTo.Concurrency != ConcurrencyParallelSafe {
		t.Fatalf("move_to Concurrency = %q, want parallel_safe", moveTo.Concurrency)
	}
	if !moveTo.Policy.ExclusivePerStep || !moveTo.Policy.SettleAfterSuccess {
		t.Fatalf("move_to Policy = %+v, want both flags true", moveTo.Policy)
	}

	if diagnostics.CapabilityRevision != 7 {
		t.Fatalf("CapabilityRevision = %d, want 7", diagnostics.CapabilityRevision)
	}
	if diagnostics.SkippedNilCapabilityCount != 1 {
		t.Fatalf("SkippedNilCapabilityCount = %d, want 1", diagnostics.SkippedNilCapabilityCount)
	}
	if got, want := diagnostics.InvalidToolNames, []string{"", " padded "}; !reflect.DeepEqual(got, want) {
		t.Fatalf("InvalidToolNames = %v, want %v", got, want)
	}
	if got, want := diagnostics.SkippedInvalidSchemaNames, []string{"array_schema", "broken_schema", "null_schema", "scalar_schema", "typed_array"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SkippedInvalidSchemaNames = %v, want %v", got, want)
	}
	if got, want := diagnostics.SkippedInvalidToolPolicyNames, []string{"bad_policy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SkippedInvalidToolPolicyNames = %v, want %v", got, want)
	}
	if got, want := diagnostics.DuplicateToolNames, []string{"duplicate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DuplicateToolNames = %v, want %v", got, want)
	}
	if got, want := diagnostics.AcceptedToolNames, []string{"emote", "move_to"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AcceptedToolNames = %v, want %v", got, want)
	}
	if diagnostics.AcceptedToolCount != 2 {
		t.Fatalf("AcceptedToolCount = %d, want 2", diagnostics.AcceptedToolCount)
	}
	if diagnostics.CatalogToolCount != 2 {
		t.Fatalf("CatalogToolCount = %d, want 2", diagnostics.CatalogToolCount)
	}
}

func TestBuildEnvironmentToolCatalogAcceptsStardewShapedCapabilities(t *testing.T) {
	capabilities := stardewShapedCapabilities(t)
	catalog, diagnostics, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Revision:     1,
		Capabilities: capabilities,
	})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}
	if catalog == nil {
		t.Fatal("catalog is nil")
	}

	wantNames := []string{"emote", "face_player", "move_to", "present_dialogue"}
	if got := toolNames(catalog.Available()); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("catalog Available names = %v, want %v", got, wantNames)
	}
	if got := toolNames(catalog.Snapshot().Available()); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("snapshot Available names = %v, want %v", got, wantNames)
	}
	if got := diagnostics.AcceptedToolNames; !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("AcceptedToolNames = %v, want %v", got, wantNames)
	}
	if diagnostics.AcceptedToolCount != 4 || diagnostics.CatalogToolCount != 4 {
		t.Fatalf("diagnostics counts = accepted %d catalog %d, want 4/4", diagnostics.AcceptedToolCount, diagnostics.CatalogToolCount)
	}

	byName := capabilitiesByName(capabilities)
	view := catalog.Snapshot()
	for _, name := range wantNames {
		catalogEntry, ok := catalog.Lookup(name)
		if !ok {
			t.Fatalf("catalog Lookup(%s) = false, want true", name)
		}
		viewEntry, ok := view.Lookup(name)
		if !ok {
			t.Fatalf("snapshot Lookup(%s) = false, want true", name)
		}
		if !reflect.DeepEqual(viewEntry, catalogEntry) {
			t.Fatalf("snapshot entry for %s = %+v, want catalog entry %+v", name, viewEntry, catalogEntry)
		}
		if catalogEntry.Definition.InputSchema != byName[name].GetInputSchemaJson() {
			t.Fatalf("%s schema was not preserved", name)
		}
		if catalogEntry.Concurrency != ConcurrencySequential {
			t.Fatalf("%s concurrency = %q, want sequential", name, catalogEntry.Concurrency)
		}
	}

	assertStardewToolMode(t, catalog, "emote", ExecutionSync, ToolPolicy{})
	assertStardewToolMode(t, catalog, "face_player", ExecutionSync, ToolPolicy{})
	assertStardewToolMode(t, catalog, "move_to", ExecutionAsync, ToolPolicy{})
	assertStardewToolMode(t, catalog, "present_dialogue", ExecutionSync, ToolPolicy{ExclusivePerStep: true, SettleAfterSuccess: true})

	assertSchemaContains(t, catalog, "emote", `"enum":["happy","sad","surprised","neutral"]`)
	assertSchemaContains(t, catalog, "present_dialogue", `"maxItems":3`, `"allow_free_text":{"type":"boolean","default":true`, `"required":["text","reply_options"]`)
	assertSchemaContains(t, catalog, "face_player", `"properties":{}`)
	assertSchemaContains(t, catalog, "move_to", `"tile"`, `"x":{"type":"integer"}`, `"y":{"type":"integer"}`)
}

func TestBuildEnvironmentToolCatalogEntityIDScopeSemantics(t *testing.T) {
	t.Run("unset entity id is environment level", func(t *testing.T) {
		catalog, diagnostics, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
			Capabilities: []*protocolv1alpha2.Capability{{Name: "speak", InputSchemaJson: `{"type":"object"}`}},
		})
		if err != nil {
			t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
		}
		if catalog == nil || diagnostics.CatalogToolCount != 1 {
			t.Fatalf("catalog=%v CatalogToolCount=%d, want explicit catalog with 1 tool", catalog, diagnostics.CatalogToolCount)
		}
	})

	t.Run("explicit empty entity id is environment level", func(t *testing.T) {
		catalog, diagnostics, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
			EntityId:     strPtr(""),
			Capabilities: []*protocolv1alpha2.Capability{{Name: "speak", InputSchemaJson: `{"type":"object"}`}},
		})
		if err != nil {
			t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
		}
		if catalog == nil || diagnostics.CatalogToolCount != 1 {
			t.Fatalf("catalog=%v CatalogToolCount=%d, want explicit catalog with 1 tool", catalog, diagnostics.CatalogToolCount)
		}
	})

	t.Run("non-empty entity id is unsupported entity scope", func(t *testing.T) {
		catalog, diagnostics, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
			EntityId:     strPtr("npc:Abigail"),
			Capabilities: []*protocolv1alpha2.Capability{{Name: "speak", InputSchemaJson: `{"type":"object"}`}},
		})
		if !errors.Is(err, ErrUnsupportedEntityScope) {
			t.Fatalf("error = %v, want ErrUnsupportedEntityScope", err)
		}
		if catalog != nil {
			t.Fatalf("catalog = %v, want nil", catalog)
		}
		if diagnostics.UnsupportedEntityID != "npc:Abigail" {
			t.Fatalf("UnsupportedEntityID = %q, want npc:Abigail", diagnostics.UnsupportedEntityID)
		}
		if diagnostics.CatalogToolCount != 0 || len(diagnostics.AcceptedToolNames) != 0 {
			t.Fatalf("diagnostics = %+v, want no catalog admission", diagnostics)
		}
	})

	t.Run("whitespace entity id is invalid entity scope", func(t *testing.T) {
		catalog, diagnostics, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
			EntityId:     strPtr(" \t "),
			Capabilities: []*protocolv1alpha2.Capability{{Name: "speak", InputSchemaJson: `{"type":"object"}`}},
		})
		if !errors.Is(err, ErrInvalidEntityScope) {
			t.Fatalf("error = %v, want ErrInvalidEntityScope", err)
		}
		if catalog != nil {
			t.Fatalf("catalog = %v, want nil", catalog)
		}
		if diagnostics.InvalidEntityID != " \t " {
			t.Fatalf("InvalidEntityID = %q, want original whitespace value", diagnostics.InvalidEntityID)
		}
		if diagnostics.CatalogToolCount != 0 || len(diagnostics.AcceptedToolNames) != 0 {
			t.Fatalf("diagnostics = %+v, want no catalog admission", diagnostics)
		}
	})
}

func TestBuildEnvironmentToolCatalogReturnsExplicitEmptyCatalog(t *testing.T) {
	catalog, diagnostics, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Capabilities: []*protocolv1alpha2.Capability{
			{Name: "", InputSchemaJson: `{"type":"object"}`},
			{Name: "broken", InputSchemaJson: `{`},
		},
	})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}
	if catalog == nil {
		t.Fatal("catalog is nil, want explicit empty catalog")
	}
	if got := catalog.Available(); len(got) != 0 {
		t.Fatalf("Available() = %v, want empty", got)
	}
	if _, ok := catalog.Lookup("broken"); ok {
		t.Fatal("Lookup(broken) = true, want false")
	}
	if diagnostics.AcceptedToolCount != 0 || diagnostics.CatalogToolCount != 0 {
		t.Fatalf("diagnostics = %+v, want zero accepted/catalog counts", diagnostics)
	}
}

func TestTurnToolViewIsSnapshotOfCatalogEntries(t *testing.T) {
	catalog, _, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Capabilities: []*protocolv1alpha2.Capability{
			{Name: "speak", InputSchemaJson: `{"type":"object"}`},
		},
	})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}

	view := catalog.Snapshot()
	catalog.tools["later"] = Entry{
		Definition:  viewToolDefinition("later"),
		Kind:        KindEnvironment,
		Concurrency: ConcurrencySequential,
		Execution:   ExecutionSync,
	}
	catalog.tools["speak"] = Entry{
		Definition:  viewToolDefinition("changed"),
		Kind:        KindEnvironment,
		Concurrency: ConcurrencySequential,
		Execution:   ExecutionSync,
	}

	if got, want := toolNames(view.Available()), []string{"speak"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot Available names = %v, want %v", got, want)
	}
	entry, ok := view.Lookup("speak")
	if !ok {
		t.Fatal("snapshot Lookup(speak) = false, want true")
	}
	if entry.Definition.Name != "speak" {
		t.Fatalf("snapshot speak name = %q, want speak", entry.Definition.Name)
	}
	if _, ok := view.Lookup("later"); ok {
		t.Fatal("snapshot Lookup(later) = true, want false")
	}
}

func TestNilEnvironmentToolCatalogFailsClearly(t *testing.T) {
	t.Run("Available", func(t *testing.T) {
		assertNilEnvironmentToolCatalogPanics(t, func(catalog *EnvironmentToolCatalog) {
			_ = catalog.Available()
		})
	})

	t.Run("Lookup", func(t *testing.T) {
		assertNilEnvironmentToolCatalogPanics(t, func(catalog *EnvironmentToolCatalog) {
			_, _ = catalog.Lookup("speak")
		})
	})

	t.Run("Snapshot", func(t *testing.T) {
		assertNilEnvironmentToolCatalogPanics(t, func(catalog *EnvironmentToolCatalog) {
			_ = catalog.Snapshot()
		})
	})
}

func TestTurnToolViewAvailableDoesNotExposeInternalSlice(t *testing.T) {
	catalog, _, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Capabilities: []*protocolv1alpha2.Capability{
			{Name: "speak", InputSchemaJson: `{"type":"object"}`},
		},
	})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}

	view := catalog.Snapshot()
	available := view.Available()
	available[0].Name = "changed"

	availableAgain := view.Available()
	if availableAgain[0].Name != "speak" {
		t.Fatalf("snapshot Available exposed internal slice: %q", availableAgain[0].Name)
	}
}

func TestBuildTurnToolViewAppliesStableCountAdmission(t *testing.T) {
	catalog := mustEnvironmentCatalog(t,
		capability("zeta", "last by name", `{"type":"object"}`),
		capability("beta", "second by name", `{"type":"object"}`),
		capability("alpha", "first by name", `{"type":"object"}`),
		capability("gamma", "third by name", `{"type":"object"}`),
	)

	result := catalog.BuildTurnToolView(ToolAdmissionConfig{
		MaxToolCount:             2,
		MaxToolDescriptionTokens: 64,
		MaxToolSchemaTokens:      64,
		MaxTotalToolSchemaTokens: 128,
	})

	if got, want := toolNames(result.View.Available()), []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("admitted tool names = %v, want %v", got, want)
	}
	if got, want := result.Report.AcceptedToolNames, []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AcceptedToolNames = %v, want %v", got, want)
	}
	assertToolAdmissionDrops(t, result.Report, []ToolAdmissionDrop{
		{Name: "gamma", Reason: ToolDropReasonCountExceeded},
		{Name: "zeta", Reason: ToolDropReasonCountExceeded},
	})
	if got, want := toolNames(catalog.Available()), []string{"alpha", "beta", "gamma", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source catalog names = %v, want unchanged %v", got, want)
	}
}

func TestBuildTurnToolViewDropsOversizedDescriptionAndSchema(t *testing.T) {
	oversizedDescription := strings.Repeat("d", 12)
	oversizedSchema := `{"type":"object","properties":{"text":{"type":"string","description":"` + strings.Repeat("s", 24) + `"}}}`
	catalog := mustEnvironmentCatalog(t,
		capability("huge_description", oversizedDescription, `{"type":"object"}`),
		capability("huge_schema", "short", oversizedSchema),
		capability("ok", "short", `{"type":"object"}`),
	)

	result := catalog.BuildTurnToolView(ToolAdmissionConfig{
		MaxToolCount:             8,
		MaxToolDescriptionTokens: tokenestimate.EstimateText(oversizedDescription) - 1,
		MaxToolSchemaTokens:      tokenestimate.EstimateText(oversizedSchema) - 1,
		MaxTotalToolSchemaTokens: 256,
	})

	if got, want := toolNames(result.View.Available()), []string{"ok"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("admitted tool names = %v, want %v", got, want)
	}
	assertToolAdmissionDrops(t, result.Report, []ToolAdmissionDrop{
		{Name: "huge_description", Reason: ToolDropReasonDescriptionTooLarge},
		{Name: "huge_schema", Reason: ToolDropReasonSchemaTooLarge},
	})
	if _, ok := result.View.Lookup("huge_description"); ok {
		t.Fatal("oversized description tool is executable, want dropped")
	}
	if _, ok := result.View.Lookup("huge_schema"); ok {
		t.Fatal("oversized schema tool is executable, want dropped")
	}
}

func TestBuildTurnToolViewAppliesTotalSchemaGreedyAdmission(t *testing.T) {
	alphaSchema := `{"type":"object","properties":{"a":{"type":"string"}}}`
	betaSchema := `{"type":"object","properties":{"b":{"type":"string"}}}`
	gammaSchema := `{"type":"object","properties":{"g":{"type":"string"}}}`
	catalog := mustEnvironmentCatalog(t,
		capability("gamma", "short", gammaSchema),
		capability("alpha", "short", alphaSchema),
		capability("beta", "short", betaSchema),
	)

	result := catalog.BuildTurnToolView(ToolAdmissionConfig{
		MaxToolCount:             8,
		MaxToolDescriptionTokens: 64,
		MaxToolSchemaTokens:      128,
		MaxTotalToolSchemaTokens: tokenestimate.EstimateText(alphaSchema) + tokenestimate.EstimateText(betaSchema),
	})

	if got, want := toolNames(result.View.Available()), []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("admitted tool names = %v, want %v", got, want)
	}
	wantTotalSchemaEstimatedTokens := tokenestimate.EstimateText(alphaSchema) + tokenestimate.EstimateText(betaSchema)
	if result.Report.TotalSchemaEstimatedTokens != wantTotalSchemaEstimatedTokens {
		t.Fatalf("TotalSchemaEstimatedTokens = %d, want %d", result.Report.TotalSchemaEstimatedTokens, wantTotalSchemaEstimatedTokens)
	}
	assertToolAdmissionDrops(t, result.Report, []ToolAdmissionDrop{
		{Name: "gamma", Reason: ToolDropReasonTotalSchemaBudgetExceeded},
	})
}

func TestBuildTurnToolViewUsesNormalizedSchemaEstimate(t *testing.T) {
	compactSchema := `{"description":"你好","type":"object"}`
	escapedSchema := `{"type":"object","description":"\u4f60\u597d"}`
	prettySchema := `{
  "type": "object",
  "description": "你好"
}`
	catalog := mustEnvironmentCatalog(t,
		capability("compact", "short", compactSchema),
		capability("escaped", "short", escapedSchema),
		capability("pretty", "short", prettySchema),
	)

	schemaTokens, err := tokenestimate.EstimateJSONDocument(compactSchema)
	if err != nil {
		t.Fatalf("EstimateJSONDocument(compactSchema) error = %v", err)
	}
	result := catalog.BuildTurnToolView(ToolAdmissionConfig{
		MaxToolCount:             8,
		MaxToolDescriptionTokens: 64,
		MaxToolSchemaTokens:      schemaTokens,
		MaxTotalToolSchemaTokens: schemaTokens * 3,
	})

	if got, want := toolNames(result.View.Available()), []string{"compact", "escaped", "pretty"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("admitted tool names = %v, want %v", got, want)
	}
	if result.Report.TotalSchemaEstimatedTokens != schemaTokens*3 {
		t.Fatalf("TotalSchemaEstimatedTokens = %d, want %d", result.Report.TotalSchemaEstimatedTokens, schemaTokens*3)
	}
	entry, ok := result.View.Lookup("pretty")
	if !ok {
		t.Fatal("pretty schema tool missing from admitted view")
	}
	if entry.Definition.InputSchema != prettySchema {
		t.Fatalf("InputSchema was rewritten:\ngot  %q\nwant %q", entry.Definition.InputSchema, prettySchema)
	}
}

func TestBuildTurnToolViewBoundsAdmissionDiagnostics(t *testing.T) {
	capabilities := make([]*protocolv1alpha2.Capability, 0, 25)
	for i := 0; i < 25; i++ {
		capabilities = append(capabilities, capability(fmt.Sprintf("tool_%02d", i), "short", `{"type":"object"}`))
	}
	catalog := mustEnvironmentCatalog(t, capabilities...)

	result := catalog.BuildTurnToolView(ToolAdmissionConfig{
		MaxToolCount:             1,
		MaxToolDescriptionTokens: 64,
		MaxToolSchemaTokens:      64,
		MaxTotalToolSchemaTokens: 1024,
	})

	if result.Report.DroppedToolCount != 24 {
		t.Fatalf("DroppedToolCount = %d, want 24", result.Report.DroppedToolCount)
	}
	if len(result.Report.DroppedToolNames) != MaxToolAdmissionDiagnosticNames {
		t.Fatalf("DroppedToolNames length = %d, want %d", len(result.Report.DroppedToolNames), MaxToolAdmissionDiagnosticNames)
	}
	if result.Report.DroppedToolNamesTruncatedCount != 24-MaxToolAdmissionDiagnosticNames {
		t.Fatalf("DroppedToolNamesTruncatedCount = %d, want %d", result.Report.DroppedToolNamesTruncatedCount, 24-MaxToolAdmissionDiagnosticNames)
	}
	if len(result.Report.DroppedTools) != MaxToolAdmissionDiagnosticNames {
		t.Fatalf("DroppedTools length = %d, want %d", len(result.Report.DroppedTools), MaxToolAdmissionDiagnosticNames)
	}
	if result.Report.DroppedToolsTruncatedCount != 24-MaxToolAdmissionDiagnosticNames {
		t.Fatalf("DroppedToolsTruncatedCount = %d, want %d", result.Report.DroppedToolsTruncatedCount, 24-MaxToolAdmissionDiagnosticNames)
	}
	if got := result.Report.DroppedReasonCounts[ToolDropReasonCountExceeded]; got != 24 {
		t.Fatalf("DroppedReasonCounts[%s] = %d, want 24", ToolDropReasonCountExceeded, got)
	}
}

func TestBuildTurnToolViewBoundsAcceptedDiagnostics(t *testing.T) {
	capabilities := make([]*protocolv1alpha2.Capability, 0, 20)
	for i := 0; i < 20; i++ {
		capabilities = append(capabilities, capability(fmt.Sprintf("tool_%02d", i), "short", `{"type":"object"}`))
	}
	catalog := mustEnvironmentCatalog(t, capabilities...)

	result := catalog.BuildTurnToolView(ToolAdmissionConfig{
		MaxToolCount:             32,
		MaxToolDescriptionTokens: 64,
		MaxToolSchemaTokens:      64,
		MaxTotalToolSchemaTokens: 2048,
	})

	if result.Report.AcceptedToolCount != 20 {
		t.Fatalf("AcceptedToolCount = %d, want 20", result.Report.AcceptedToolCount)
	}
	if len(result.Report.AcceptedToolNames) != MaxToolAdmissionDiagnosticNames {
		t.Fatalf("AcceptedToolNames length = %d, want %d", len(result.Report.AcceptedToolNames), MaxToolAdmissionDiagnosticNames)
	}
	if result.Report.AcceptedToolNamesTruncatedCount != 20-MaxToolAdmissionDiagnosticNames {
		t.Fatalf("AcceptedToolNamesTruncatedCount = %d, want %d", result.Report.AcceptedToolNamesTruncatedCount, 20-MaxToolAdmissionDiagnosticNames)
	}
	if got, want := len(result.View.Available()), 20; got != want {
		t.Fatalf("admitted tool count = %d, want %d", got, want)
	}
}

func TestBuildTurnToolViewBoundsDiagnosticNameLength(t *testing.T) {
	acceptedName := "aaa_" + strings.Repeat("accepted_", MaxToolAdmissionDiagnosticNameRunes)
	droppedName := "zzz_" + strings.Repeat("dropped_", MaxToolAdmissionDiagnosticNameRunes)
	catalog := mustEnvironmentCatalog(t,
		capability(acceptedName, "short", `{"type":"object"}`),
		capability(droppedName, "short", `{"type":"object"}`),
	)

	result := catalog.BuildTurnToolView(ToolAdmissionConfig{
		MaxToolCount:             1,
		MaxToolDescriptionTokens: 64,
		MaxToolSchemaTokens:      64,
		MaxTotalToolSchemaTokens: 1024,
	})

	if _, ok := result.View.Lookup(acceptedName); !ok {
		t.Fatal("accepted tool missing under its full name")
	}
	if got := result.View.Available()[0].Name; got != acceptedName {
		t.Fatalf("model-visible tool name = %q, want full name", got)
	}
	if got := result.Report.AcceptedToolNames[0]; got == acceptedName || runeCount(got) > MaxToolAdmissionDiagnosticNameRunes || !strings.HasSuffix(got, "...") {
		t.Fatalf("AcceptedToolNames[0] = %q, want bounded display name", got)
	}
	if got := result.Report.DroppedToolNames[0]; got == droppedName || runeCount(got) > MaxToolAdmissionDiagnosticNameRunes || !strings.HasSuffix(got, "...") {
		t.Fatalf("DroppedToolNames[0] = %q, want bounded display name", got)
	}
	if got := result.Report.DroppedTools[0].Name; got == droppedName || runeCount(got) > MaxToolAdmissionDiagnosticNameRunes || !strings.HasSuffix(got, "...") {
		t.Fatalf("DroppedTools[0].Name = %q, want bounded display name", got)
	}
}

func TestToolAdmissionDiagnosticNameTruncationIsRuneSafe(t *testing.T) {
	name := strings.Repeat("名", MaxToolAdmissionDiagnosticNameRunes)

	got := diagnosticToolName(name + "tail")

	if runeCount(got) > MaxToolAdmissionDiagnosticNameRunes || !strings.HasSuffix(got, "...") {
		t.Fatalf("diagnosticToolName length/suffix = %q, want bounded name with suffix", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("diagnosticToolName produced invalid UTF-8: %q", got)
	}
}

func invalidToolPolicyExtensions(t *testing.T) *structpb.Struct {
	t.Helper()
	extensions, err := structpb.NewStruct(map[string]any{
		"gameagent": map[string]any{
			"tool_policy": map[string]any{
				"exclusive_per_step": "yes",
			},
		},
	})
	if err != nil {
		t.Fatalf("build invalid extensions: %v", err)
	}
	return extensions
}

func toolPolicyExtensions(t *testing.T, exclusivePerStep bool, settleAfterSuccess bool) *structpb.Struct {
	t.Helper()

	extensions, err := structpb.NewStruct(map[string]any{
		"gameagent": map[string]any{
			"tool_policy": map[string]any{
				"exclusive_per_step":   exclusivePerStep,
				"settle_after_success": settleAfterSuccess,
			},
		},
	})
	if err != nil {
		t.Fatalf("build extensions: %v", err)
	}
	return extensions
}

func mustEnvironmentCatalog(t *testing.T, capabilities ...*protocolv1alpha2.Capability) *EnvironmentToolCatalog {
	t.Helper()

	catalog, _, err := BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{Capabilities: capabilities})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}
	return catalog
}

func capability(name string, description string, schema string) *protocolv1alpha2.Capability {
	return &protocolv1alpha2.Capability{
		Name:            name,
		Description:     description,
		InputSchemaJson: schema,
	}
}

func assertToolAdmissionDrops(t *testing.T, report ToolAdmissionReport, want []ToolAdmissionDrop) {
	t.Helper()

	if got := report.DroppedTools; !reflect.DeepEqual(got, want) {
		t.Fatalf("DroppedTools = %+v, want %+v", got, want)
	}
	if report.DroppedToolCount != len(want) {
		t.Fatalf("DroppedToolCount = %d, want %d", report.DroppedToolCount, len(want))
	}
}

func runeCount(value string) int {
	return len([]rune(value))
}

func assertNilEnvironmentToolCatalogPanics(t *testing.T, access func(*EnvironmentToolCatalog)) {
	t.Helper()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("catalog access did not panic")
		}
		if got, want := recovered, "environment tool catalog is nil"; got != want {
			t.Fatalf("panic = %v, want %q", got, want)
		}
	}()

	var catalog *EnvironmentToolCatalog
	access(catalog)
}

func strPtr(value string) *string {
	return &value
}

func toolNames(tools []model.ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func viewToolDefinition(name string) model.ToolDefinition {
	return model.ToolDefinition{
		Name:        name,
		Description: strings.ToUpper(name),
		InputSchema: `{"type":"object"}`,
	}
}

func stardewShapedCapabilities(t *testing.T) []*protocolv1alpha2.Capability {
	t.Helper()

	return []*protocolv1alpha2.Capability{
		{
			Name:            "emote",
			Version:         "0.1.0",
			Description:     "Displays one emote bubble above the NPC.",
			InputSchemaJson: `{"type":"object","properties":{"emote":{"type":"string","enum":["happy","sad","surprised","neutral"]}},"required":["emote"],"additionalProperties":false}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
			ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL,
		},
		{
			Name:            "present_dialogue",
			Version:         "0.1.0",
			Description:     "Displays NPC dialogue using exactly one of two forms. For continuing dialogue, provide exactly three distinct player-authored reply options and set allow_free_text=true or omit it because true is the default. For ending dialogue, provide reply_options=[] and allow_free_text=false. It must be the only tool call in its model response. After it succeeds, the current turn ends; wait for player_said_to_npc before continuing that conversation.",
			InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string","maxLength":240},"reply_options":{"type":"array","maxItems":3,"items":{"type":"string","maxLength":80},"description":"Exactly three distinct player replies for continuing dialogue; empty only for ending dialogue."},"allow_free_text":{"type":"boolean","default":true,"description":"True or omitted for continuing dialogue; explicit false only for ending dialogue."}},"required":["text","reply_options"],"additionalProperties":false}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
			ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL,
			Extensions:      toolPolicyExtensions(t, true, true),
		},
		{
			Name:            "face_player",
			Version:         "0.1.0",
			Description:     "Turns the NPC to face the player when both are in the same location.",
			InputSchemaJson: `{"type":"object","properties":{},"additionalProperties":false}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
			ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL,
		},
		{
			Name:            "move_to",
			Version:         "0.1.0",
			Description:     "Moves the NPC toward a reachable tile in the current location. The action is asynchronous; wait for the terminal result before deciding the next step.",
			InputSchemaJson: `{"type":"object","properties":{"location":{"type":"string"},"tile":{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"],"additionalProperties":false}},"required":["location","tile"],"additionalProperties":false}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_ASYNC,
			ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL,
		},
	}
}

func capabilitiesByName(capabilities []*protocolv1alpha2.Capability) map[string]*protocolv1alpha2.Capability {
	byName := make(map[string]*protocolv1alpha2.Capability, len(capabilities))
	for _, capability := range capabilities {
		byName[capability.GetName()] = capability
	}
	return byName
}

func assertStardewToolMode(t *testing.T, catalog *EnvironmentToolCatalog, name string, execution ExecutionMode, policy ToolPolicy) {
	t.Helper()

	entry, ok := catalog.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%s) = false, want true", name)
	}
	if entry.Execution != execution {
		t.Fatalf("%s execution = %q, want %q", name, entry.Execution, execution)
	}
	if entry.Policy != policy {
		t.Fatalf("%s policy = %+v, want %+v", name, entry.Policy, policy)
	}
}

func assertSchemaContains(t *testing.T, catalog *EnvironmentToolCatalog, name string, values ...string) {
	t.Helper()

	entry, ok := catalog.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%s) = false, want true", name)
	}
	for _, value := range values {
		if !strings.Contains(entry.Definition.InputSchema, value) {
			t.Fatalf("%s schema missing %q: %s", name, value, entry.Definition.InputSchema)
		}
	}
}
