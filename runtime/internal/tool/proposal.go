package tool

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math"
	"strings"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
)

type taskProposal struct {
	Clock                task.Clock      `json:"clock"`
	WakeAt               int64           `json:"wake_at"`
	DeadlineAt           int64           `json:"deadline_at"`
	ParticipantEntityIDs []string        `json:"participant_entity_ids"`
	EquivalenceKey       string          `json:"equivalence_key"`
	Payload              json.RawMessage `json:"payload,omitempty"`
}

func normalizeTaskProposal(value *protocol.TaskProposal) (taskProposal, error) {
	if value == nil {
		return taskProposal{}, task.ErrInvalidTaskSpec
	}
	clock := task.Clock{ID: value.GetClock().GetClockId(), Tick: value.GetClock().GetNowTick(), Sequence: value.GetClock().GetSequence()}
	if clock.Validate() != nil || clock.Sequence > math.MaxInt64 || value.GetWakeAt() <= clock.Tick || value.GetWakeAt() > value.GetDeadlineAt() || value.GetEquivalenceKey() != "" && strings.TrimSpace(value.GetEquivalenceKey()) == "" {
		return taskProposal{}, task.ErrInvalidTaskSpec
	}
	participants := append([]string(nil), value.GetParticipantEntityIds()...)
	for _, id := range participants {
		if strings.TrimSpace(id) == "" {
			return taskProposal{}, task.ErrInvalidTaskSpec
		}
	}
	var payload json.RawMessage
	if value.GetPayload() != nil {
		var err error
		payload, err = json.Marshal(value.GetPayload().AsMap())
		if err != nil {
			return taskProposal{}, task.ErrInvalidTaskSpec
		}
	}
	return taskProposal{Clock: clock, WakeAt: value.GetWakeAt(), DeadlineAt: value.GetDeadlineAt(), ParticipantEntityIDs: participants, EquivalenceKey: value.GetEquivalenceKey(), Payload: payload}, nil
}

func (t *TaskTools) CaptureProposal(ctx context.Context, rc RuntimeCallContext, result *protocol.ActionResult) (string, error) {
	if result.GetStatus() != protocol.ActionStatus_ACTION_STATUS_SUCCEEDED || result.GetTaskProposal() == nil {
		return "", nil
	}
	proposal, err := normalizeTaskProposal(result.GetTaskProposal())
	if err != nil {
		return "", nil
	}
	ref := ""
	err = t.guard(ctx, rc, func(exec task.ExecutionContext) error {
		if exec.Source.Kind != task.SourceKindInteraction {
			return task.ErrSourceInvalid
		}
		if proposal.Clock.ID != exec.Clock.ID {
			return task.ErrClockMismatch
		}
		if proposal.Clock.Tick > exec.Clock.Tick || proposal.Clock.Sequence > exec.Clock.Sequence || exec.Clock.Tick >= proposal.WakeAt {
			return nil
		}
		ref = "proposal_" + rand.Text()
		t.proposals[ref] = proposal
		return nil
	})
	return ref, err
}
