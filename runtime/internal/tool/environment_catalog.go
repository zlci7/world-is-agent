package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/tokenestimate"
)

var (
	ErrMissingCapabilityList  = errors.New("missing capability list")
	ErrUnsupportedEntityScope = errors.New("unsupported entity scope")
	ErrInvalidEntityScope     = errors.New("invalid entity scope")
)

type BootstrapDiagnostics struct {
	AcceptedToolCount             int
	AcceptedToolNames             []string
	SkippedNilCapabilityCount     int
	InvalidToolNames              []string
	SkippedInvalidSchemaNames     []string
	SkippedInvalidToolPolicyNames []string
	DuplicateToolNames            []string
	UnsupportedEntityID           string
	InvalidEntityID               string
	CapabilityRevision            uint64
	CatalogToolCount              int
}

type ToolAdmissionConfig struct {
	MaxToolCount             int
	MaxToolDescriptionTokens int
	MaxToolSchemaTokens      int
	MaxTotalToolSchemaTokens int
}

type ToolAdmissionResult struct {
	View   TurnToolView
	Report ToolAdmissionReport
}

type ToolAdmissionReport struct {
	AcceptedToolCount               int
	AcceptedToolNames               []string
	AcceptedToolNamesTruncatedCount int
	DroppedToolCount                int
	DroppedToolNames                []string
	DroppedToolNamesTruncatedCount  int
	DroppedTools                    []ToolAdmissionDrop
	DroppedToolsTruncatedCount      int
	DroppedReasonCounts             map[string]int
	TotalSchemaEstimatedTokens      int
}

type ToolAdmissionDrop struct {
	Name   string
	Reason string
}

const (
	ToolDropReasonCountExceeded             = "tool_count_exceeded"
	ToolDropReasonDescriptionTooLarge       = "tool_description_too_large"
	ToolDropReasonSchemaTooLarge            = "tool_schema_too_large"
	ToolDropReasonTotalSchemaBudgetExceeded = "tool_total_schema_budget_exceeded"

	MaxToolAdmissionDiagnosticNames     = 16
	MaxToolAdmissionDiagnosticNameRunes = 128

	defaultMaxToolCount             = 64
	defaultMaxToolDescriptionTokens = 2048
	defaultMaxToolSchemaTokens      = 8192
	defaultMaxTotalToolSchemaTokens = 32768
)

type EnvironmentToolCatalog struct {
	tools          map[string]Entry
	duplicateNames []string
}

type TurnToolView struct {
	tools          map[string]Entry
	runtimeContext *RuntimeCallContext
}

func BuildEnvironmentToolCatalog(list *protocolv1alpha2.CapabilityList) (*EnvironmentToolCatalog, BootstrapDiagnostics, error) {
	if list == nil {
		return nil, BootstrapDiagnostics{}, ErrMissingCapabilityList
	}

	diagnostics := BootstrapDiagnostics{
		CapabilityRevision: list.GetRevision(),
	}
	if list.EntityId != nil {
		entityID := *list.EntityId
		if entityID != "" {
			if strings.TrimSpace(entityID) == "" {
				diagnostics.InvalidEntityID = entityID
				return nil, diagnostics, fmt.Errorf("%w: %q", ErrInvalidEntityScope, entityID)
			}
			diagnostics.UnsupportedEntityID = entityID
			return nil, diagnostics, fmt.Errorf("%w: %q", ErrUnsupportedEntityScope, entityID)
		}
	}

	groups := make(map[string][]*protocolv1alpha2.Capability)
	for _, capability := range list.GetCapabilities() {
		if capability == nil {
			diagnostics.SkippedNilCapabilityCount++
			continue
		}
		if !isValidCapabilityName(capability.GetName()) {
			diagnostics.InvalidToolNames = append(diagnostics.InvalidToolNames, capability.GetName())
			continue
		}
		groups[capability.GetName()] = append(groups[capability.GetName()], capability)
	}

	tools := make(map[string]Entry)
	names := sortedMapKeys(groups)
	for _, name := range names {
		capabilities := groups[name]
		if len(capabilities) > 1 {
			diagnostics.DuplicateToolNames = append(diagnostics.DuplicateToolNames, name)
			continue
		}

		capability := capabilities[0]
		if !isObjectInputSchema(capability.GetInputSchemaJson()) {
			diagnostics.SkippedInvalidSchemaNames = append(diagnostics.SkippedInvalidSchemaNames, name)
			continue
		}
		policy, ok := toolPolicyFromCapability(capability)
		if !ok {
			diagnostics.SkippedInvalidToolPolicyNames = append(diagnostics.SkippedInvalidToolPolicyNames, name)
			continue
		}

		tools[name] = environmentEntryFromCapability(capability, policy)
		diagnostics.AcceptedToolNames = append(diagnostics.AcceptedToolNames, name)
	}

	sort.Strings(diagnostics.InvalidToolNames)
	sort.Strings(diagnostics.SkippedInvalidSchemaNames)
	sort.Strings(diagnostics.SkippedInvalidToolPolicyNames)
	sort.Strings(diagnostics.DuplicateToolNames)
	sort.Strings(diagnostics.AcceptedToolNames)
	diagnostics.AcceptedToolCount = len(diagnostics.AcceptedToolNames)
	diagnostics.CatalogToolCount = len(tools)

	return &EnvironmentToolCatalog{tools: tools, duplicateNames: append([]string(nil), diagnostics.DuplicateToolNames...)}, diagnostics, nil
}

func (c *EnvironmentToolCatalog) Available() []model.ToolDefinition {
	requireEnvironmentToolCatalog(c)
	return availableToolDefinitions(c.tools)
}

func (c *EnvironmentToolCatalog) Lookup(name string) (Entry, bool) {
	requireEnvironmentToolCatalog(c)
	entry, ok := c.tools[name]
	return entry, ok
}

func (c *EnvironmentToolCatalog) Snapshot() TurnToolView {
	requireEnvironmentToolCatalog(c)
	return TurnToolView{tools: cloneEntries(c.tools)}
}

func (c *EnvironmentToolCatalog) BuildTurnToolView(config ToolAdmissionConfig) ToolAdmissionResult {
	requireEnvironmentToolCatalog(c)
	return buildTurnToolView(c.tools, config)
}

func buildTurnToolView(entries map[string]Entry, config ToolAdmissionConfig) ToolAdmissionResult {
	config = config.withDefaults()

	admitted := make(map[string]Entry)
	report := ToolAdmissionReport{}
	totalSchemaEstimatedTokens := 0

	for _, name := range sortedMapKeys(entries) {
		entry := entries[name]
		descriptionEstimatedTokens := tokenestimate.EstimateText(entry.Definition.Description)
		schemaEstimatedTokens, err := tokenestimate.EstimateJSONDocument(entry.Definition.InputSchema)
		if err != nil {
			panic(fmt.Sprintf("estimate tool schema tokens: %v", err))
		}

		switch {
		case descriptionEstimatedTokens > config.MaxToolDescriptionTokens:
			report.addDrop(name, ToolDropReasonDescriptionTooLarge)
			continue
		case schemaEstimatedTokens > config.MaxToolSchemaTokens:
			report.addDrop(name, ToolDropReasonSchemaTooLarge)
			continue
		case len(admitted) >= config.MaxToolCount:
			report.addDrop(name, ToolDropReasonCountExceeded)
			continue
		case totalSchemaEstimatedTokens+schemaEstimatedTokens > config.MaxTotalToolSchemaTokens:
			report.addDrop(name, ToolDropReasonTotalSchemaBudgetExceeded)
			continue
		}

		admitted[name] = entry
		totalSchemaEstimatedTokens += schemaEstimatedTokens
		report.addAccepted(name)
	}

	report.TotalSchemaEstimatedTokens = totalSchemaEstimatedTokens
	return ToolAdmissionResult{
		View:   TurnToolView{tools: admitted},
		Report: report,
	}
}

func requireEnvironmentToolCatalog(c *EnvironmentToolCatalog) {
	if c == nil {
		panic("environment tool catalog is nil")
	}
}

func (v TurnToolView) Available() []model.ToolDefinition {
	return availableToolDefinitions(v.tools)
}

func (v TurnToolView) Lookup(name string) (Entry, bool) {
	entry, ok := v.tools[name]
	return entry, ok
}

func environmentEntryFromCapability(capability *protocolv1alpha2.Capability, policy ToolPolicy) Entry {
	return Entry{
		Definition: model.ToolDefinition{
			Name:        capability.GetName(),
			Description: capability.GetDescription(),
			InputSchema: capability.GetInputSchemaJson(),
		},
		Kind:        KindEnvironment,
		Concurrency: concurrencyModeFromCapability(capability),
		Execution:   executionModeFromCapability(capability),
		Policy:      policy,
	}
}

func isValidCapabilityName(name string) bool {
	return name != "" && name == strings.TrimSpace(name)
}

func isObjectInputSchema(schemaJSON string) bool {
	var schema map[string]json.RawMessage
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return false
	}
	if schema == nil {
		return false
	}

	rawType, ok := schema["type"]
	if !ok {
		return true
	}
	var rootType string
	if err := json.Unmarshal(rawType, &rootType); err != nil {
		return false
	}
	return rootType == "object"
}

func availableToolDefinitions(entries map[string]Entry) []model.ToolDefinition {
	available := make([]model.ToolDefinition, 0, len(entries))
	for _, entry := range entries {
		available = append(available, entry.Definition)
	}
	sort.Slice(available, func(i, j int) bool {
		return available[i].Name < available[j].Name
	})
	return available
}

func cloneEntries(entries map[string]Entry) map[string]Entry {
	cloned := make(map[string]Entry, len(entries))
	for name, entry := range entries {
		cloned[name] = entry
	}
	return cloned
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (c ToolAdmissionConfig) withDefaults() ToolAdmissionConfig {
	c.MaxToolCount = positiveOrDefault(c.MaxToolCount, defaultMaxToolCount)
	c.MaxToolDescriptionTokens = positiveOrDefault(c.MaxToolDescriptionTokens, defaultMaxToolDescriptionTokens)
	c.MaxToolSchemaTokens = positiveOrDefault(c.MaxToolSchemaTokens, defaultMaxToolSchemaTokens)
	c.MaxTotalToolSchemaTokens = positiveOrDefault(c.MaxTotalToolSchemaTokens, defaultMaxTotalToolSchemaTokens)
	return c
}

func (r *ToolAdmissionReport) addDrop(name string, reason string) {
	r.DroppedToolCount++
	displayName := diagnosticToolName(name)
	if len(r.DroppedToolNames) < MaxToolAdmissionDiagnosticNames {
		r.DroppedToolNames = append(r.DroppedToolNames, displayName)
	} else {
		r.DroppedToolNamesTruncatedCount++
	}
	if len(r.DroppedTools) < MaxToolAdmissionDiagnosticNames {
		r.DroppedTools = append(r.DroppedTools, ToolAdmissionDrop{Name: displayName, Reason: reason})
	} else {
		r.DroppedToolsTruncatedCount++
	}
	if r.DroppedReasonCounts == nil {
		r.DroppedReasonCounts = make(map[string]int)
	}
	r.DroppedReasonCounts[reason]++
}

func (r *ToolAdmissionReport) addAccepted(name string) {
	r.AcceptedToolCount++
	if len(r.AcceptedToolNames) < MaxToolAdmissionDiagnosticNames {
		r.AcceptedToolNames = append(r.AcceptedToolNames, diagnosticToolName(name))
	} else {
		r.AcceptedToolNamesTruncatedCount++
	}
}

func diagnosticToolName(name string) string {
	const suffix = "..."
	runes := []rune(name)
	if len(runes) <= MaxToolAdmissionDiagnosticNameRunes {
		return name
	}
	keep := MaxToolAdmissionDiagnosticNameRunes - len([]rune(suffix))
	return string(runes[:keep]) + suffix
}

func positiveOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
