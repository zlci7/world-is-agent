package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tokenestimate"
)

type HistorySourceRef struct {
	ID          string
	Sequence    int64
	Fingerprint string
}

type SummaryCheckpoint struct {
	ID                  string
	Owner               session.AgentSessionKey
	ParentID            string
	Revision            int64
	Version             string
	Text                string
	Sources             []HistorySourceRef
	Times               []HistoryTime
	CreatedAt           time.Time
	CoverageFingerprint string

	sourceCount       int
	coverageTimesJSON string
}

type SummaryReadLimits struct {
	Sources     int
	Checkpoints int
	Bytes       int
}

type SummaryRead struct {
	Checkpoint  *SummaryCheckpoint
	Diagnostics []string
	ReadBytes   int
}

type SummaryCommit struct {
	Snapshot         HistorySnapshot
	ExpectedRevision int64
	ParentID         string
	Version          string
	Text             string
	Sources          []HistorySourceRef
	MaxOutputTokens  int
}

type SummaryStore interface {
	ReadSummary(context.Context, HistorySnapshot, *GameTimeSnapshot, SummaryReadLimits) (SummaryRead, error)
	CommitSummary(context.Context, SummaryCommit) (SummaryCheckpoint, error)
}

const (
	maxSummarySources     = 16384
	maxSummaryCheckpoints = 64
	defaultSummaryTokens  = 2048
)

func boundedSummaryReadLimits(store HistoryLimits, requested SummaryReadLimits) SummaryReadLimits {
	result := SummaryReadLimits{Sources: min(store.SummarySources, maxSummarySources), Checkpoints: min(store.SummaryCheckpoints, maxSummaryCheckpoints), Bytes: store.ScanBytes}
	if requested.Sources > 0 {
		result.Sources = min(result.Sources, requested.Sources)
	}
	if requested.Checkpoints > 0 {
		result.Checkpoints = min(result.Checkpoints, requested.Checkpoints)
	}
	if requested.Bytes > 0 {
		result.Bytes = min(result.Bytes, requested.Bytes)
	}
	return result
}

type summaryScanBudget struct {
	sources int
	bytes   int
}

func (b *summaryScanBudget) takeBytes(size int) error {
	if size < 0 || size > b.bytes {
		return fmt.Errorf("%w: summary scan bytes", ErrHistoryCapacity)
	}
	b.bytes -= size
	return nil
}

// Accessors reserve metadata bytes before loading headers and references.
type summaryAccess struct {
	checkpoint func(id string, before int64, budget *summaryScanBudget) (*SummaryCheckpoint, error)
	references func(id string, budget *summaryScanBudget) ([]HistorySourceRef, error)
	sources    func(ctx context.Context, ids []string, budget *summaryScanBudget) ([]HistorySource, error)
}

func validateSummaryCommit(request SummaryCommit, limits HistoryLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if request.ExpectedRevision < 0 || request.ExpectedRevision != request.Snapshot.SummaryRevision {
		return ErrSummaryConflict
	}
	if strings.TrimSpace(request.Version) == "" || strings.TrimSpace(request.Text) == "" {
		return fmt.Errorf("%w: summary text and generation version are required", ErrInvalidHistory)
	}
	maxTokens := request.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultSummaryTokens
	}
	if len(request.Text) > limits.ScanBytes || len(request.Version) > limits.ScanBytes-len(request.Text) || tokenestimate.EstimateText(request.Text) > maxTokens {
		return fmt.Errorf("%w: summary output exceeds budget", ErrHistoryCapacity)
	}
	if len(request.Sources) > min(limits.SummarySources, maxSummarySources) {
		return fmt.Errorf("%w: summary input references", ErrHistoryCapacity)
	}
	return nil
}

func canonicalSummarySources(snapshot HistorySnapshot, inherited, added []HistorySourceRef, limit int) ([]HistorySourceRef, error) {
	byID := make(map[string]HistorySourceRef)
	bySequence := make(map[int64]string)
	for _, group := range [][]HistorySourceRef{inherited, added} {
		for _, ref := range group {
			if ref.ID == "" || ref.Fingerprint == "" || ref.Sequence <= 0 || ref.Sequence > snapshot.Watermark {
				return nil, fmt.Errorf("%w: invalid summary source reference", ErrSummaryConflict)
			}
			if previous, ok := byID[ref.ID]; ok && previous != ref {
				return nil, fmt.Errorf("%w: inconsistent duplicate source", ErrSummaryConflict)
			}
			if previous, ok := bySequence[ref.Sequence]; ok && previous != ref.ID {
				return nil, fmt.Errorf("%w: inconsistent source sequence", ErrSummaryConflict)
			}
			byID[ref.ID] = ref
			bySequence[ref.Sequence] = ref.ID
			if len(byID) > limit {
				return nil, fmt.Errorf("%w: cumulative summary references", ErrHistoryCapacity)
			}
		}
	}
	if len(byID) == 0 {
		return nil, fmt.Errorf("%w: summary coverage is required", ErrInvalidHistory)
	}
	refs := make([]HistorySourceRef, 0, len(byID))
	for _, ref := range byID {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Sequence < refs[j].Sequence })
	return refs, nil
}

func validateSummaryHeader(checkpoint *SummaryCheckpoint, snapshot HistorySnapshot) error {
	if checkpoint == nil || checkpoint.ID == "" || checkpoint.Owner != snapshot.Owner || checkpoint.Revision <= 0 || checkpoint.Revision > snapshot.SummaryRevision || strings.TrimSpace(checkpoint.Version) == "" || strings.TrimSpace(checkpoint.Text) == "" {
		return fmt.Errorf("%w: summary checkpoint identity or snapshot", ErrSummaryConflict)
	}
	if checkpoint.sourceCount <= 0 || checkpoint.sourceCount > maxSummarySources || len(checkpoint.CoverageFingerprint) != 64 {
		return fmt.Errorf("%w: summary coverage manifest", ErrInvalidHistory)
	}
	return nil
}

func summaryCoverageFingerprint(refs []HistorySourceRef, times []HistoryTime) (string, error) {
	type sourceRef struct {
		ID          string `json:"id"`
		Sequence    int64  `json:"sequence"`
		Fingerprint string `json:"fingerprint"`
	}
	manifest := struct {
		Sources []sourceRef   `json:"sources"`
		Times   []HistoryTime `json:"times"`
	}{Sources: make([]sourceRef, len(refs)), Times: times}
	// References follow sequence order; each source retains its original time order.
	for i, ref := range refs {
		manifest.Sources[i] = sourceRef{ID: ref.ID, Sequence: ref.Sequence, Fingerprint: ref.Fingerprint}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return sha256LowerHex(string(data)), nil
}

func sealSummaryCoverage(checkpoint *SummaryCheckpoint) error {
	timesJSON, err := json.Marshal(checkpoint.Times)
	if err != nil {
		return err
	}
	fingerprint, err := summaryCoverageFingerprint(checkpoint.Sources, checkpoint.Times)
	if err != nil {
		return err
	}
	checkpoint.sourceCount = len(checkpoint.Sources)
	checkpoint.coverageTimesJSON = string(timesJSON)
	checkpoint.CoverageFingerprint = fingerprint
	return nil
}

func decodeSummaryCoverageTimes(checkpoint *SummaryCheckpoint) error {
	if err := json.Unmarshal([]byte(checkpoint.coverageTimesJSON), &checkpoint.Times); err != nil {
		return fmt.Errorf("%w: summary coverage times", ErrInvalidHistory)
	}
	return nil
}

func readSummaryCoverage(ctx context.Context, snapshot HistorySnapshot, checkpoint *SummaryCheckpoint, access summaryAccess, budget *summaryScanBudget, sourceLimit int) error {
	refs, err := access.references(checkpoint.ID, budget)
	if err != nil {
		return err
	}
	if len(refs) != checkpoint.sourceCount {
		return fmt.Errorf("%w: summary source count differs from manifest", ErrInvalidHistory)
	}
	refs, err = canonicalSummarySources(snapshot, nil, refs, sourceLimit)
	if err != nil {
		return err
	}
	if len(refs) != checkpoint.sourceCount {
		return fmt.Errorf("%w: duplicate summary coverage references", ErrInvalidHistory)
	}
	fingerprint, err := summaryCoverageFingerprint(refs, checkpoint.Times)
	if err != nil {
		return err
	}
	if fingerprint != checkpoint.CoverageFingerprint {
		return fmt.Errorf("%w: summary coverage fingerprint", ErrInvalidHistory)
	}
	times, err := resolveSummaryTimes(ctx, snapshot, refs, access, budget)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(times, checkpoint.Times) {
		return fmt.Errorf("%w: summary source times differ from manifest", ErrInvalidHistory)
	}
	checkpoint.Sources = refs
	return nil
}

func resolveSummaryTimes(ctx context.Context, snapshot HistorySnapshot, refs []HistorySourceRef, access summaryAccess, budget *summaryScanBudget) ([]HistoryTime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	sources, err := access.sources(ctx, ids, budget)
	if err != nil {
		return nil, err
	}
	if len(sources) != len(refs) {
		return nil, fmt.Errorf("%w: incomplete summary source heads", ErrInvalidHistory)
	}
	var times []HistoryTime
	for i, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source := sources[i]
		if source.Owner != snapshot.Owner || source.ID != ref.ID || source.Sequence != ref.Sequence || source.Fingerprint != ref.Fingerprint || source.Sequence > snapshot.Watermark {
			return nil, fmt.Errorf("%w: summary source identity", ErrSummaryConflict)
		}
		if source.Availability != HistoryAvailable && source.Availability != HistoryPruned {
			return nil, fmt.Errorf("%w: summary source head availability", ErrInvalidHistory)
		}
		if len(source.Times) == 0 {
			times = append(times, HistoryTime{})
		} else {
			times = append(times, cloneHistoryTimes(source.Times)...)
		}
	}
	return times, nil
}

func prepareSummaryCheckpoint(ctx context.Context, request SummaryCommit, limits HistoryLimits, access summaryAccess) (SummaryCheckpoint, error) {
	readLimits := boundedSummaryReadLimits(limits, SummaryReadLimits{})
	budget := summaryScanBudget{sources: readLimits.Sources, bytes: readLimits.Bytes}
	readSources := access.sources
	verifiedSources := make(map[string]HistorySource)
	access.sources = func(ctx context.Context, ids []string, budget *summaryScanBudget) ([]HistorySource, error) {
		var missing []string
		for _, id := range ids {
			if _, ok := verifiedSources[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) != 0 {
			sources, err := readSources(ctx, missing, budget)
			if err != nil {
				return nil, err
			}
			if len(sources) != len(missing) {
				return nil, fmt.Errorf("%w: incomplete summary source heads", ErrInvalidHistory)
			}
			for i, source := range sources {
				verifiedSources[missing[i]] = source
			}
		}
		sources := make([]HistorySource, len(ids))
		for i, id := range ids {
			sources[i] = verifiedSources[id]
		}
		return sources, nil
	}
	var inherited []HistorySourceRef
	if request.ParentID != "" {
		parent, err := access.checkpoint(request.ParentID, 0, &budget)
		if err != nil {
			return SummaryCheckpoint{}, err
		}
		if err = validateSummaryHeader(parent, request.Snapshot); err != nil {
			return SummaryCheckpoint{}, err
		}
		if err := readSummaryCoverage(ctx, request.Snapshot, parent, access, &budget, readLimits.Sources); err != nil {
			return SummaryCheckpoint{}, err
		}
		inherited = parent.Sources
	}
	refs, err := canonicalSummarySources(request.Snapshot, inherited, request.Sources, readLimits.Sources)
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	checkpoint := SummaryCheckpoint{Owner: request.Snapshot.Owner, ParentID: request.ParentID, Version: request.Version, Text: request.Text, Sources: refs}
	checkpoint.Times, err = resolveSummaryTimes(ctx, request.Snapshot, refs, access, &budget)
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	if err := sealSummaryCoverage(&checkpoint); err != nil {
		return SummaryCheckpoint{}, err
	}
	if err := budget.takeBytes(summaryHeaderBytes(checkpoint)); err != nil {
		return SummaryCheckpoint{}, err
	}
	return checkpoint, nil
}

func readSummaryWithAccess(ctx context.Context, snapshot HistorySnapshot, currentTime *GameTimeSnapshot, limits SummaryReadLimits, access summaryAccess) (read SummaryRead, err error) {
	budget := summaryScanBudget{sources: limits.Sources, bytes: limits.Bytes}
	defer func() { read.ReadBytes = limits.Bytes - budget.bytes }()
	var before int64
	for attempt := 0; attempt < limits.Checkpoints; attempt++ {
		if err := ctx.Err(); err != nil {
			return read, err
		}
		checkpoint, err := access.checkpoint("", before, &budget)
		if checkpoint != nil {
			before = checkpoint.Revision
		}
		if err != nil {
			if checkpoint != nil && summaryReadFailure(&read, err) {
				continue
			}
			return read, err
		}
		if checkpoint == nil {
			return read, nil
		}
		err = validateSummaryHeader(checkpoint, snapshot)
		if err == nil {
			err = readSummaryCoverage(ctx, snapshot, checkpoint, access, &budget, limits.Sources)
		}
		if err != nil {
			if summaryReadFailure(&read, err) {
				continue
			}
			return read, err
		}
		visible, unknown := HistoryVisibility(checkpoint.Times, currentTime)
		if unknown {
			addSummaryDiagnostic(&read, "summary_time_unknown")
		}
		if !visible {
			addSummaryDiagnostic(&read, "summary_time_hidden")
			continue
		}
		read.Checkpoint = checkpoint
		return read, nil
	}
	addSummaryDiagnostic(&read, "summary_scan_incomplete")
	return read, nil
}

func summaryReadFailure(read *SummaryRead, err error) bool {
	switch {
	case errors.Is(err, ErrHistoryCapacity):
		addSummaryDiagnostic(read, "summary_scan_incomplete")
	case errors.Is(err, ErrInvalidHistory), errors.Is(err, ErrSummaryConflict), errors.Is(err, ErrHistoryNotFound):
		addSummaryDiagnostic(read, "summary_coverage_invalid")
	default:
		return false
	}
	return true
}

func addSummaryDiagnostic(read *SummaryRead, diagnostic string) {
	for _, existing := range read.Diagnostics {
		if existing == diagnostic {
			return
		}
	}
	read.Diagnostics = append(read.Diagnostics, diagnostic)
}

func summaryHeaderBytes(checkpoint SummaryCheckpoint) int {
	return len(checkpoint.ID) + len(checkpoint.ParentID) + len(checkpoint.Version) + len(checkpoint.Text) + len(checkpoint.Owner.GameID) + len(checkpoint.Owner.WorldID) + len(checkpoint.Owner.EntityID) + len(checkpoint.CoverageFingerprint) + len(checkpoint.coverageTimesJSON) + 24
}

func summaryReferenceBytes(ref HistorySourceRef) int {
	return len(ref.ID) + len(ref.Fingerprint) + 8
}

func summarySourceHeadBytes(source HistorySource, timesBytes int) int {
	return len(source.ID) + len(source.Fingerprint) + len(source.Availability) + timesBytes + 8
}

func cloneHistoryTimes(times []HistoryTime) []HistoryTime {
	if times == nil {
		return nil
	}
	cloned := append([]HistoryTime{}, times...)
	for i := range cloned {
		if cloned[i].Value != nil {
			value := *cloned[i].Value
			cloned[i].Value = &value
		}
	}
	return cloned
}

func cloneSummaryCheckpoint(checkpoint SummaryCheckpoint) SummaryCheckpoint {
	checkpoint.Sources = append([]HistorySourceRef(nil), checkpoint.Sources...)
	checkpoint.Times = cloneHistoryTimes(checkpoint.Times)
	return checkpoint
}
