package gateway

import (
	"context"
	"errors"
	"log"
	"strings"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
)

// checkpointPrepareReply turns a prepare request into the reply the Adapter waits on. The reply
// always echoes the request scope and save_request_id so a pending Adapter request can resolve
// without guessing, and carries the fenced generation only when the snapshot was accepted.
func (s *Server) checkpointPrepareReply(ctx context.Context, env *streamEnvironment, request *protocol.CheckpointPrepare) *protocol.CheckpointPrepared {
	reply := &protocol.CheckpointPrepared{Scope: request.GetScope(), SaveRequestId: strings.TrimSpace(request.GetSaveRequestId())}
	binding, bindingErr := taskBindingFromProtocol(request.GetScope())
	clock, clockErr := taskClockFromProtocol(request.GetClock())
	if bindingErr != nil {
		reply.Error = taskErrorToProtocol(bindingErr)
		return reply
	}
	if clockErr != nil {
		reply.Error = taskErrorToProtocol(clockErr)
		return reply
	}
	if reply.SaveRequestId == "" {
		reply.Error = taskErrorToProtocol(task.ErrInvalidTaskSpec)
		return reply
	}
	prepared, err := s.worlds.PrepareCheckpoint(ctx, env, binding, clock, reply.SaveRequestId, request.GetFinalEvidence())
	if err != nil {
		reply.Error = taskErrorToProtocol(err)
		return reply
	}
	reference, err := taskCheckpointToProtocol(prepared.Reference)
	if err != nil {
		reply.Error = taskErrorToProtocol(err)
		return reply
	}
	reply.Scope = taskScopeToProtocol(prepared.Head.Binding)
	reply.Checkpoint = reference
	return reply
}

// PrepareCheckpoint hosts the Adapter save handoff. The in-memory barrier rises before the clock
// sample is synchronized, and both the clock update and the snapshot commit run inside one world
// critical section so the stored reference names the exact clock the Adapter observed.
func (r *WorldRegistry) PrepareCheckpoint(ctx context.Context, env *streamEnvironment, binding task.Binding, clock task.Clock, saveRequestID string, finalEvidence []*protocol.TaskEvidence) (task.Prepared, error) {
	slot := r.slot(binding.World, false)
	if slot == nil {
		return task.Prepared{}, task.ErrWorldNotReady
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.saving {
		if slot.owner == nil || slot.owner.env != env || slot.saveRequestID != saveRequestID {
			return task.Prepared{}, task.ErrSaveInProgress
		}
		// The Adapter repeated a request the world already prepared: the kernel keeps the
		// original scope, clock and evidence and returns the same reference.
		evidence, err := checkpointEvidence(finalEvidence)
		if err != nil {
			return task.Prepared{}, err
		}
		return r.service.PrepareCheckpoint(ctx, binding, clock, saveRequestID, evidence)
	}
	if err := slot.check(env, binding); err != nil {
		return task.Prepared{}, err
	}
	slot.saving = true
	slot.saveRequestID = saveRequestID
	slot.authorityEpoch++
	evidence, err := checkpointEvidence(finalEvidence)
	if err != nil {
		r.releasePrepareBarrier(slot)
		return task.Prepared{}, err
	}
	if _, err := r.service.UpdateClock(ctx, binding, clock); err != nil {
		r.releasePrepareBarrier(slot)
		return task.Prepared{}, err
	}
	prepared, err := r.service.PrepareCheckpoint(ctx, binding, clock, saveRequestID, evidence)
	if err != nil {
		r.releasePrepareBarrier(slot)
		return task.Prepared{}, err
	}
	slot.head = prepared.Head
	slot.ready = false
	return prepared, nil
}

// checkpointEvidence maps the final evidence a save carries. Its source identity follows the
// durable fact so a fact delivered through a checkpoint and through Observe collapse to one.
func checkpointEvidence(values []*protocol.TaskEvidence) ([]task.Evidence, error) {
	evidence := make([]task.Evidence, 0, len(values))
	for _, value := range values {
		converted, err := taskEvidenceFromProtocol(value, task.SourceRef{Kind: task.SourceKindEnvironment, CallID: value.GetFactId()})
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, converted)
	}
	return evidence, nil
}

// releasePrepareBarrier drops the in-memory barrier after the snapshot was refused. Admission
// stays closed until a valid WorldBinding succeeds, so no action can cross the refused save.
func (r *WorldRegistry) releasePrepareBarrier(slot *worldSlot) {
	slot.saving = false
	slot.saveRequestID = ""
	slot.ready = false
	slot.authorityEpoch++
}

// FinishCheckpoint releases the barrier for a finished save. The kernel is the authority on the
// request identity and generation, so a Finish that names another save is rejected without
// touching the barrier of the current one.
func (r *WorldRegistry) FinishCheckpoint(ctx context.Context, binding task.Binding, saveRequestID string, saved bool) error {
	slot := r.slot(binding.World, false)
	if slot == nil {
		return task.ErrWorldNotReady
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.owner == nil {
		return task.ErrGenerationStale
	}
	if err := r.service.FinishCheckpoint(ctx, binding, saveRequestID, saved); err != nil {
		return err
	}
	if slot.saving && slot.saveRequestID == saveRequestID {
		slot.saving = false
		slot.saveRequestID = ""
	}
	return nil
}

// releaseExpiredSaveBarrier retires a kernel barrier that outlived its bound and reports whether
// the world is free of one. That is how a save which ended without a Finish becomes bindable
// again: the kernel owns the bound, and the caller only drops its own in-memory barrier.
func (r *WorldRegistry) releaseExpiredSaveBarrier(ctx context.Context, world task.WorldKey) error {
	barrier, err := r.service.ReadCheckpointBarrier(ctx, world)
	if err != nil {
		return err
	}
	if barrier.Held {
		return task.ErrSaveInProgress
	}
	return nil
}

// finishCheckpoint applies a CheckpointFinish message and reports only failures, because a
// released barrier needs no acknowledgement.
func (s *Server) finishCheckpoint(ctx context.Context, conn *worldConnection, finish *protocol.CheckpointFinish) error {
	saveRequestID := strings.TrimSpace(finish.GetSaveRequestId())
	if saveRequestID == "" {
		return task.ErrInvalidTaskSpec
	}
	binding, err := taskBindingFromProtocol(finish.GetScope())
	if err != nil {
		return err
	}
	err = s.worlds.FinishCheckpoint(ctx, binding, saveRequestID, finish.GetSaved())
	if errors.Is(err, task.ErrSaveInProgress) {
		log.Printf("task save barrier holds world game=%q world=%q save_request_id=%q owner=%s", binding.World.GameID, binding.World.WorldID, saveRequestID, conn.id)
	}
	return err
}
