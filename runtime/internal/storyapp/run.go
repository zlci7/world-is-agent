package storyapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"gameagent/runtime/internal/model"
)

type npcDecision struct {
	Speech       string `json:"speech"`
	ActionIntent string `json:"action_intent"`
	Silent       bool   `json:"silent"`
	Memory       string `json:"memory"`
}

type turnIntent struct {
	IntentType  string `json:"intent_type"`
	AddresseeID string `json:"addressee_id"`
	Visibility  string `json:"visibility"`
}

type hostResult struct {
	TimeMinutes     int                `json:"time_minutes"`
	Scene           string             `json:"scene"`
	SceneCharacters []string           `json:"scene_characters"`
	Outcomes        []hostActionResult `json:"outcomes"`
}

type hostActionResult struct {
	ActionID   string   `json:"action_id"`
	Status     string   `json:"status"`
	Content    string   `json:"content"`
	Recipients []string `json:"recipients"`
}

type narrativeResult struct {
	Narrative string `json:"narrative"`
}

type turnStage string

const (
	turnStageLoad         turnStage = "load_world"
	turnStageIntent       turnStage = "intent"
	turnStageNPC          turnStage = "npc"
	turnStageCoordination turnStage = "coordination"
	turnStageNarration    turnStage = "narration"
	turnStageCommit       turnStage = "commit"

	structuredTurnOutputTokens = 4096
)

type turnStageError struct {
	Stage turnStage
	Err   error
}

func (e *turnStageError) Error() string { return string(e.Stage) + ": " + e.Err.Error() }
func (e *turnStageError) Unwrap() error { return e.Err }

func atTurnStage(stage turnStage, err error) error {
	if err == nil {
		return nil
	}
	return &turnStageError{Stage: stage, Err: err}
}

func stageOf(err error) string {
	var staged *turnStageError
	if errors.As(err, &staged) {
		return string(staged.Stage)
	}
	return "unknown"
}

func classifyTurnFailure(err error) (status, reason, message string) {
	if errors.Is(err, context.Canceled) {
		return "cancelled", "cancelled", "the turn was cancelled"
	}
	if errors.Is(err, ErrVersionConflict) {
		return "failed", "version_conflict", "the world changed before this turn could be saved"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "failed", "generation_timeout", "the model response timed out"
	}
	if errors.Is(err, ErrModelNotConfigured) {
		return "failed", "model_not_configured", "the model connection is unavailable"
	}
	var staged *turnStageError
	if errors.As(err, &staged) {
		switch staged.Stage {
		case turnStageLoad, turnStageCommit:
			return "failed", "storage_unavailable", "the turn could not access its save data"
		case turnStageIntent:
			return "failed", "intent_generation_failed", "the player intent could not be understood"
		case turnStageNPC:
			return "failed", "npc_generation_failed", "one or more characters could not respond"
		case turnStageCoordination:
			return "failed", "coordination_generation_failed", "the scene outcome could not be resolved"
		case turnStageNarration:
			return "failed", "narration_generation_failed", "the story response could not be written"
		}
	}
	return "failed", "generation_failed", "the turn did not complete"
}

type turnOutput struct {
	Narrative       string
	Clock           string
	Scene           string
	SceneVersion    int64
	SceneCharacters []string
	Events          []Event
	Perceptions     []Perception
	Memories        []Memory
}

func (a *App) SubmitRun(ctx context.Context, worldID string, request RunRequest) (Run, error) {
	request.Input = cleanText(request.Input)
	request.RequestKey = strings.TrimSpace(request.RequestKey)
	request.AddresseeID = cleanText(request.AddresseeID)
	if request.RequestKey == "" || request.Input == "" || len([]rune(request.Input)) > 4000 {
		return Run{}, ErrInvalidRequest
	}
	path, status, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return Run{}, err
	}
	world := a.worldRuntimeFor(worldID)
	world.mu.Lock()
	defer world.mu.Unlock()
	store, err := openWorldDB(path)
	if err != nil {
		return Run{}, err
	}
	defer store.db.Close()
	if existing, found, err := readRunByRequest(ctx, store.db, request.RequestKey); err != nil {
		return Run{}, err
	} else if found {
		if existing.RequestHash != a.hashRun(request) {
			return Run{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	activeID, activeRevision, err := a.activeWorld(ctx)
	if err != nil || activeID != worldID {
		return Run{}, ErrVersionConflict
	}
	if request.ExpectedActiveRevision > 0 && request.ExpectedActiveRevision != activeRevision {
		return Run{}, ErrVersionConflict
	}
	if status != "ready" {
		return Run{}, ErrWorldNotReady
	}
	a.modelMu.RLock()
	generator := a.generator
	a.modelMu.RUnlock()
	if generator == nil {
		return Run{}, ErrModelNotConfigured
	}
	if world.savePending {
		return Run{}, ErrWorldBusy
	}
	snapshot, err := loadWorldSnapshot(ctx, store, 1)
	if err != nil {
		return Run{}, err
	}
	if (request.requireBaseline || request.ExpectedMessageHead > 0) && request.ExpectedMessageHead != snapshot.Summary.MessageHead {
		return Run{}, ErrVersionConflict
	}
	if (request.requireBaseline || request.ExpectedEventHead > 0) && request.ExpectedEventHead != snapshot.Summary.EventHead {
		return Run{}, ErrVersionConflict
	}
	if (request.requireBaseline || request.ExpectedContextEpoch > 0) && request.ExpectedContextEpoch != snapshot.Summary.ContextEpoch {
		return Run{}, ErrVersionConflict
	}
	if request.requireBaseline && (request.expectedTurnSeq != snapshot.Summary.TurnSeq || request.expectedSceneVersion != snapshot.SceneVersion) {
		return Run{}, ErrVersionConflict
	}
	if request.AddresseeID != "" {
		if _, ok := findSceneCharacter(snapshot.Characters, request.AddresseeID); !ok {
			return Run{}, ErrInvalidRequest
		}
	}
	if count, err := countActiveRuns(ctx, store.db); err != nil {
		return Run{}, err
	} else if count > 0 {
		return Run{}, ErrWorldBusy
	}
	now := time.Now().UTC()
	attempt := request.attempt
	if attempt < 1 {
		attempt = 1
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	inputID := cleanText(request.inputID)
	inputSeq := request.inputSeq
	if inputSeq > 0 {
		if inputID == "" {
			return Run{}, ErrVersionConflict
		}
		var latestInputSeq int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(input_seq),0) FROM runs`).Scan(&latestInputSeq); err != nil {
			return Run{}, err
		}
		if latestInputSeq != inputSeq {
			return Run{}, ErrVersionConflict
		}
		var completed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE input_id=? AND status='completed'`, inputID).Scan(&completed); err != nil {
			return Run{}, err
		}
		if completed > 0 {
			return Run{}, ErrVersionConflict
		}
	} else {
		value, err := metaGetTx(ctx, tx, "input_seq")
		if err != nil {
			return Run{}, err
		}
		inputSeq, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Run{}, err
		}
		inputSeq++
		inputID = newID("input")
		if err := metaSetTx(ctx, tx, "input_seq", strconv.FormatInt(inputSeq, 10)); err != nil {
			return Run{}, err
		}
	}
	run := Run{
		RunID: newID("run"), RequestKey: request.RequestKey, RequestHash: a.hashRun(request),
		Input: request.Input, AddresseeID: cleanText(request.AddresseeID), Attempt: attempt,
		Status: "accepted", CreatedAt: now, UpdatedAt: now,
		InputID: inputID, InputSeq: inputSeq,
		BaseTurnSeq: snapshot.Summary.TurnSeq, BaseMessageHead: snapshot.Summary.MessageHead,
		BaseEventHead: snapshot.Summary.EventHead, BaseContextEpoch: snapshot.Summary.ContextEpoch,
		BaseSceneVersion: snapshot.SceneVersion,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(
		run_id,request_key,request_hash,input,addressee_id,attempt,status,input_id,input_seq,base_turn_seq,base_message_head,base_event_head,base_context_epoch,base_scene_version,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.RunID, run.RequestKey, run.RequestHash, run.Input,
		run.AddresseeID, run.Attempt, run.Status, run.InputID, run.InputSeq, run.BaseTurnSeq, run.BaseMessageHead, run.BaseEventHead,
		run.BaseContextEpoch, run.BaseSceneVersion, run.CreatedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	runtime := &runRuntime{Cancel: cancel, Done: make(chan struct{}), WorldID: worldID, RunID: run.RunID, ActiveRevision: activeRevision, Generator: generator}
	a.runsMu.Lock()
	a.runs[run.RunID] = runtime
	a.runsMu.Unlock()
	go a.runWorker(runCtx, runtime, run)
	return run, nil
}

func (a *App) runWorker(ctx context.Context, runtime *runRuntime, run Run) {
	defer close(runtime.Done)
	defer func() {
		a.runsMu.Lock()
		delete(a.runs, run.RunID)
		a.runsMu.Unlock()
	}()

	path, _, err := a.worldRecord(context.Background(), runtime.WorldID)
	if err != nil {
		a.logRunFailure(runtime.WorldID, run, "open_world", "storage_unavailable", err)
		return
	}
	store, err := openWorldDB(path)
	if err != nil {
		a.logRunFailure(runtime.WorldID, run, "open_world", "storage_unavailable", err)
		return
	}
	defer store.db.Close()
	if err := updateRunStatus(context.Background(), store.db, run.RunID, "running", "", ""); err != nil {
		a.logRunFailure(runtime.WorldID, run, "mark_running", "storage_unavailable", err)
		return
	}
	if !a.isActive(context.Background(), runtime.WorldID, runtime.ActiveRevision) {
		_ = updateRunStatus(context.Background(), store.db, run.RunID, "cancelled", "world_switched", "the active world changed")
		return
	}
	output, err := a.executeTurn(ctx, store, run, runtime.Generator)
	if err != nil {
		status, reason, message := classifyTurnFailure(err)
		if status == "failed" {
			a.logRunFailure(runtime.WorldID, run, stageOf(err), reason, err)
		}
		if updateErr := updateRunStatus(context.Background(), store.db, run.RunID, status, reason, message); updateErr != nil {
			a.logRunFailure(runtime.WorldID, run, "record_failure", "storage_unavailable", updateErr)
		}
		return
	}
	if !a.isActive(context.Background(), runtime.WorldID, runtime.ActiveRevision) {
		_ = updateRunStatus(context.Background(), store.db, run.RunID, "cancelled", "world_switched", "the active world changed")
		return
	}
	if _, err := commitTurn(ctx, store, run, output.Narrative, output.Events, output.Perceptions, output.Memories, output.Clock, output.Scene, output.SceneVersion, output.SceneCharacters); err != nil {
		err = atTurnStage(turnStageCommit, err)
		status, reason, message := classifyTurnFailure(err)
		if status == "failed" {
			a.logRunFailure(runtime.WorldID, run, string(turnStageCommit), reason, err)
		}
		if updateErr := updateRunStatus(context.Background(), store.db, run.RunID, status, reason, message); updateErr != nil {
			a.logRunFailure(runtime.WorldID, run, "record_failure", "storage_unavailable", updateErr)
		}
		return
	}
	_ = a.touchWorld(context.Background(), runtime.WorldID)
}

func (a *App) logRunFailure(worldID string, run Run, stage, reason string, err error) {
	if a.logger == nil || err == nil {
		return
	}
	a.logger.Printf("story turn failed: world_id=%q run_id=%q attempt=%d stage=%q reason=%q error=%v", worldID, run.RunID, run.Attempt, stage, reason, err)
}

func (a *App) Run(ctx context.Context, worldID, runID string) (Run, error) {
	path, status, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return Run{}, err
	}
	if status != "ready" {
		return Run{}, ErrWorldNotReady
	}
	store, err := openWorldDB(path)
	if err != nil {
		return Run{}, err
	}
	defer store.db.Close()
	run, found, err := readRun(ctx, store.db, runID)
	if err != nil {
		return Run{}, err
	}
	if !found {
		return Run{}, ErrRunNotFound
	}
	return run, nil
}

func (a *App) ListRuns(ctx context.Context, worldID string) ([]Run, error) {
	path, status, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return nil, err
	}
	if status != "ready" {
		return nil, ErrWorldNotReady
	}
	store, err := openWorldDB(path)
	if err != nil {
		return nil, err
	}
	defer store.db.Close()
	rows, err := store.db.QueryContext(ctx, `SELECT run_id,request_key,request_hash,input,addressee_id,attempt,status,reason,error,message_seq,input_id,input_seq,base_turn_seq,base_message_head,base_event_head,base_context_epoch,base_scene_version,created_at,updated_at FROM runs ORDER BY created_at DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, found, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		if found {
			runs = append(runs, run)
		}
	}
	return runs, rows.Err()
}

func (a *App) CancelRun(ctx context.Context, worldID, runID string) error {
	path, _, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return err
	}
	store, err := openWorldDB(path)
	if err != nil {
		return err
	}
	defer store.db.Close()
	if _, err := store.db.ExecContext(ctx, `UPDATE runs SET cancel_requested=1,updated_at=? WHERE run_id=? AND status IN ('accepted','running')`, nowText(), runID); err != nil {
		return err
	}
	a.runsMu.Lock()
	runtime := a.runs[runID]
	a.runsMu.Unlock()
	if runtime != nil {
		runtime.Cancel()
	}
	return nil
}

func (a *App) RetryRun(ctx context.Context, worldID, runID, requestKey string) (Run, error) {
	path, _, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return Run{}, err
	}
	store, err := openWorldDB(path)
	if err != nil {
		return Run{}, err
	}
	run, found, err := readRun(ctx, store.db, runID)
	_ = store.db.Close()
	if err != nil {
		return Run{}, err
	}
	if !found {
		return Run{}, ErrRunNotFound
	}
	if run.Status != "failed" && run.Status != "cancelled" && run.Status != "interrupted" {
		return Run{}, ErrInvalidRequest
	}
	if run.InputID == "" || run.InputSeq <= 0 {
		return Run{}, ErrVersionConflict
	}
	if strings.TrimSpace(requestKey) == "" {
		requestKey = newID("retry")
	}
	return a.SubmitRun(ctx, worldID, RunRequest{
		RequestKey: requestKey, Input: run.Input, AddresseeID: run.AddresseeID, attempt: run.Attempt + 1,
		ExpectedMessageHead: run.BaseMessageHead, ExpectedEventHead: run.BaseEventHead,
		ExpectedContextEpoch: run.BaseContextEpoch, requireBaseline: true,
		expectedTurnSeq: run.BaseTurnSeq, expectedSceneVersion: run.BaseSceneVersion,
		inputID: run.InputID, inputSeq: run.InputSeq,
	})
}

func resolveTurnIntent(ctx context.Context, generator model.TextGenerator, snapshot worldSnapshot, run Run) (turnIntent, error) {
	participants := sceneCharacters(snapshot.Characters)
	explicitRecipient := cleanText(run.AddresseeID)
	if explicitRecipient != "" {
		if _, ok := findSceneCharacter(participants, explicitRecipient); !ok {
			return turnIntent{}, ErrInvalidRequest
		}
	}
	var characters strings.Builder
	for _, character := range participants {
		fmt.Fprintf(&characters, "- %s：%s（%s）\n", character.EntityID, character.Name, character.Role)
	}
	input := fmt.Sprintf("当前地点：%s\n当前时间：%s\n在场人物：\n%s玩家输入：%s\n显式目标（若有）：%s\n请判断玩家本轮是 speak、observe 还是 act；如果玩家明确向某个在场人物说话，只返回该人物的 entity_id；没有明确对象时 addressee_id 返回空字符串或 null。visibility 只能是 public 或 private。只输出 JSON：{\"intent_type\":\"speak\",\"addressee_id\":\"npc:...\",\"visibility\":\"public\"}。人物名出现在谈话内容里不等于玩家正在对该人物说话。", snapshot.Summary.Scene, snapshot.Summary.Clock, characters.String(), run.Input, explicitRecipient)
	var intent turnIntent
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := generateJSONWithNullableFields(callCtx, generator, "你负责把玩家本轮输入解析成结构化回合意图。只根据输入和在场名单判断目标、可见范围与意图类型，不替玩家执行行动。", input, &intent, structuredTurnOutputTokens, []string{"addressee_id"}, "intent_type", "addressee_id", "visibility"); err != nil {
		return turnIntent{}, err
	}
	intent.IntentType = strings.ToLower(cleanText(intent.IntentType))
	intent.Visibility = strings.ToLower(cleanText(intent.Visibility))
	intent.AddresseeID = cleanText(intent.AddresseeID)
	if explicitRecipient != "" {
		intent.AddresseeID = explicitRecipient
	}
	if intent.IntentType == "" {
		return turnIntent{}, fmt.Errorf("%w: intent_type is empty", ErrGenerationFailed)
	}
	if intent.IntentType != "speak" && intent.IntentType != "observe" && intent.IntentType != "act" {
		return turnIntent{}, fmt.Errorf("%w: invalid intent_type %q", ErrGenerationFailed, intent.IntentType)
	}
	if intent.AddresseeID != "" {
		if _, ok := findSceneCharacter(participants, intent.AddresseeID); !ok {
			return turnIntent{}, ErrInvalidRequest
		}
	}
	if intent.Visibility != "public" && intent.Visibility != "private" {
		return turnIntent{}, fmt.Errorf("%w: invalid visibility %q", ErrGenerationFailed, intent.Visibility)
	}
	if intent.Visibility == "private" && intent.AddresseeID == "" {
		return turnIntent{}, fmt.Errorf("%w: private intent has no addressee", ErrGenerationFailed)
	}
	return intent, nil
}

func (a *App) executeTurn(ctx context.Context, store *worldStore, run Run, generator model.TextGenerator) (turnOutput, error) {
	snapshot, err := loadWorldSnapshot(ctx, store, 40)
	if err != nil {
		return turnOutput{}, atTurnStage(turnStageLoad, err)
	}
	def := lanternDefinition()
	intent, err := resolveTurnIntent(ctx, generator, snapshot, run)
	if err != nil {
		return turnOutput{}, atTurnStage(turnStageIntent, err)
	}
	recipient := intent.AddresseeID
	private := intent.Visibility == "private"
	participants := sceneCharacters(snapshot.Characters)
	now := time.Now().UTC()
	playerEventID := run.RunID + ":input"
	output := turnOutput{
		Clock: snapshot.Summary.Clock, Scene: snapshot.Summary.Scene, SceneVersion: snapshot.SceneVersion,
		SceneCharacters: characterIDs(participants),
		Events:          []Event{{EventID: playerEventID, EventType: "player_attempt", ActorID: "player", TargetID: recipient, Content: run.Input, RunID: run.RunID, Stage: 1, SceneVersion: snapshot.SceneVersion, SourceType: "player", CreatedAt: now}},
		Memories:        []Memory{}, Perceptions: []Perception{},
	}
	decisions := make(map[string]npcDecision)
	perceptText := make(map[string]string)
	for _, character := range participants {
		if private && character.EntityID != recipient {
			perceptText[character.EntityID] = fmt.Sprintf("你看见玩家与%s低声交谈，但听不清内容。不要猜测耳语原文。", describeRecipient(def, recipient))
		} else {
			perceptText[character.EntityID] = run.Input
		}
		output.Perceptions = append(output.Perceptions, Perception{RecipientID: character.EntityID, SourceEventID: playerEventID, SourceType: sourceTypeFor(private, character.EntityID, recipient), Content: perceptText[character.EntityID], Stage: 1, SceneVersion: snapshot.SceneVersion, CreatedAt: now})
	}
	if err := a.decideNPCs(ctx, generator, snapshot, def, recipient, intent.IntentType, perceptText, nil, decisions, 1, ""); err != nil {
		return turnOutput{}, atTurnStage(turnStageNPC, err)
	}
	var publicReplyLog []string
	for _, character := range participants {
		decision := decisions[character.EntityID]
		if reply := appendNPCDecisionOutput(&output, run, character, decision, participants, perceptText[character.EntityID], playerEventID, snapshot.SceneVersion, 1); reply != "" {
			publicReplyLog = append(publicReplyLog, reply)
		}
	}

	// A public answer is a new stimulus. Every present NPC gets one bounded chance to react to it.
	if len(publicReplyLog) > 0 {
		follow := strings.Join(publicReplyLog, "\n")
		for _, character := range participants {
			previous := decisions[character.EntityID]
			followDecisions := make(map[string]npcDecision)
			followText := map[string]string{character.EntityID: "公开回应的新刺激：" + follow}
			priorTurn := map[string]string{character.EntityID: fmt.Sprintf("第一阶段感知：%s\n第一阶段自己的决定：%s", perceptText[character.EntityID], formatSelfDecision(previous))}
			if err := a.decideNPCs(ctx, generator, snapshot, def, recipient, intent.IntentType, followText, priorTurn, followDecisions, 2, follow); err != nil {
				return turnOutput{}, atTurnStage(turnStageNPC, err)
			}
			decision, ok := followDecisions[character.EntityID]
			if !ok {
				continue
			}
			if reply := appendNPCDecisionOutput(&output, run, character, decision, participants, followText[character.EntityID], playerEventID, snapshot.SceneVersion, 2); reply != "" {
				publicReplyLog = append(publicReplyLog, reply)
			}
			decisions[character.EntityID] = mergeNPCDecision(previous, decision)
		}
	}

	publicReplies := strings.Join(publicReplyLog, "\n")
	host, err := a.coordinateTurn(ctx, generator, snapshot, run, intent, decisions, output.Events, publicReplies)
	if err != nil {
		return turnOutput{}, atTurnStage(turnStageCoordination, err)
	}
	output.Clock = advanceClock(snapshot.Summary.Clock, run.Input, host.TimeMinutes)
	output.Scene = host.Scene
	output.SceneCharacters = append([]string(nil), host.SceneCharacters...)
	if output.Scene != snapshot.Summary.Scene {
		output.SceneVersion = snapshot.SceneVersion + 1
	}
	visibleOutcomes, err := appendHostOutcomes(&output, run, participants, host.Outcomes)
	if err != nil {
		return turnOutput{}, atTurnStage(turnStageCoordination, err)
	}
	playerProjection := joinVisibleResults(publicReplies, visibleOutcomes)
	result, err := a.narrateVisible(ctx, generator, snapshot, run, def, recipient, intent.IntentType, playerProjection, private, output.Clock, output.Scene, output.SceneCharacters)
	if err != nil {
		return turnOutput{}, atTurnStage(turnStageNarration, err)
	}
	output.Narrative = result.Narrative
	output.Events = append(output.Events, Event{EventID: run.RunID + ":outcome", EventType: "turn_settled", ActorID: "scene", Content: playerProjection, RunID: run.RunID, Stage: 3, SceneVersion: output.SceneVersion, SourceType: "scene", CreatedAt: time.Now().UTC()})
	for _, character := range participants {
		memory := "玩家说：" + run.Input
		kind := "heard_player"
		if private && character.EntityID == recipient {
			memory = "玩家私下告诉我：" + run.Input
		} else if private {
			kind = "observed"
			memory = "我看见玩家和" + describeRecipient(def, recipient) + "低声交谈，但没有听清内容。"
		}
		output.Memories = append(output.Memories, Memory{RecipientID: character.EntityID, Kind: kind, Content: memory, SourceEventID: playerEventID, CreatedAt: time.Now().UTC()})
	}
	return output, nil
}

func appendNPCDecisionOutput(output *turnOutput, run Run, character Character, decision npcDecision, participants []Character, perception, playerEventID string, sceneVersion int64, stage int) string {
	sourceEventID := playerEventID
	if decision.ActionIntent != "" {
		actionEventID := fmt.Sprintf("%s:%s:action:%d", run.RunID, character.EntityID, stage)
		output.Events = append(output.Events, Event{EventID: actionEventID, EventType: "npc_action_intent", ActorID: character.EntityID, TargetID: "player", Content: decision.ActionIntent, RunID: run.RunID, Stage: stage, SceneVersion: sceneVersion, SourceType: "npc_intent", CreatedAt: time.Now().UTC()})
		sourceEventID = actionEventID
	}
	var reply string
	if decision.Speech != "" {
		eventID := fmt.Sprintf("%s:%s:speech:%d", run.RunID, character.EntityID, stage)
		output.Events = append(output.Events, Event{EventID: eventID, EventType: "npc_dialogue", ActorID: character.EntityID, TargetID: "player", Content: decision.Speech, RunID: run.RunID, Stage: stage, SceneVersion: sceneVersion, SourceType: "visible_dialogue", CreatedAt: time.Now().UTC()})
		for _, other := range participants {
			if other.EntityID != character.EntityID {
				output.Perceptions = append(output.Perceptions, Perception{RecipientID: other.EntityID, SourceEventID: eventID, SourceType: "heard_public_reply", Content: fmt.Sprintf("%s（%s）公开说：%s", character.Name, character.Role, decision.Speech), Stage: stage, SceneVersion: sceneVersion, CreatedAt: time.Now().UTC()})
			}
		}
		if decision.ActionIntent == "" {
			sourceEventID = eventID
		}
		reply = fmt.Sprintf("%s（%s）说：%s", character.Name, character.Role, decision.Speech)
	}
	if decision.Memory != "" {
		output.Memories = append(output.Memories, Memory{RecipientID: character.EntityID, Kind: "character_judgment", Content: cleanText(decision.Memory), SourceEventID: sourceEventID, CreatedAt: time.Now().UTC()})
	}
	return reply
}

func appendHostOutcomes(output *turnOutput, run Run, participants []Character, outcomes []hostActionResult) ([]string, error) {
	actions := make(map[string]Event)
	for _, event := range output.Events {
		if event.EventType == "npc_action_intent" {
			actions[event.EventID] = event
		}
	}
	if len(outcomes) != len(actions) {
		return nil, fmt.Errorf("%w: outcome count %d does not match action count %d", ErrGenerationFailed, len(outcomes), len(actions))
	}
	participantIDs := make(map[string]bool, len(participants))
	for _, character := range participants {
		participantIDs[character.EntityID] = true
	}
	seen := make(map[string]bool, len(outcomes))
	var visible []string
	for index, outcome := range outcomes {
		outcome.ActionID = cleanText(outcome.ActionID)
		outcome.Status = strings.ToLower(cleanText(outcome.Status))
		outcome.Content = cleanText(outcome.Content)
		action, ok := actions[outcome.ActionID]
		if !ok || seen[outcome.ActionID] || outcome.Content == "" || (outcome.Status != "succeeded" && outcome.Status != "failed" && outcome.Status != "partial") || outcome.Recipients == nil {
			return nil, fmt.Errorf("%w: invalid outcome at index %d", ErrGenerationFailed, index)
		}
		seen[outcome.ActionID] = true
		recipients := make(map[string]bool, len(outcome.Recipients)+1)
		for _, id := range outcome.Recipients {
			id = cleanText(id)
			if id != "player" && !participantIDs[id] {
				return nil, fmt.Errorf("%w: outcome %q has unknown recipient %q", ErrGenerationFailed, outcome.ActionID, id)
			}
			recipients[id] = true
		}
		// An actor always observes the resolved result of its own attempt.
		recipients[action.ActorID] = true
		resultID := fmt.Sprintf("%s:result:%d", outcome.ActionID, index+1)
		output.Events = append(output.Events, Event{EventID: resultID, EventType: "npc_action_result", ActorID: action.ActorID, TargetID: action.TargetID, Content: outcome.Content, RunID: run.RunID, Stage: 3, SceneVersion: output.SceneVersion, SourceType: "action_" + outcome.Status, CreatedAt: time.Now().UTC()})
		for _, character := range participants {
			if recipients[character.EntityID] {
				output.Perceptions = append(output.Perceptions, Perception{RecipientID: character.EntityID, SourceEventID: resultID, SourceType: "action_" + outcome.Status, Content: outcome.Content, Stage: 3, SceneVersion: output.SceneVersion, CreatedAt: time.Now().UTC()})
			}
		}
		if recipients["player"] {
			visible = append(visible, outcome.Content)
		}
	}
	return visible, nil
}

func joinVisibleResults(publicReplies string, outcomes []string) string {
	parts := make([]string, 0, len(outcomes)+1)
	if value := cleanText(publicReplies); value != "" {
		parts = append(parts, value)
	}
	for _, outcome := range outcomes {
		if value := cleanText(outcome); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return "没有人立刻回答，也没有发生玩家可见的新结果。"
	}
	return strings.Join(parts, "\n")
}

func mergeNPCDecision(previous, current npcDecision) npcDecision {
	if current.Speech == "" {
		current.Speech = previous.Speech
	}
	if current.ActionIntent == "" {
		current.ActionIntent = previous.ActionIntent
	}
	if current.Memory == "" {
		current.Memory = previous.Memory
	}
	current.Silent = current.Speech == ""
	return current
}

func advanceClock(clock, input string, minutes int) string {
	if minutes == 0 && !strings.Contains(input, "等待") && !strings.Contains(strings.ToLower(input), "wait") {
		return clock
	}
	if minutes == 0 {
		minutes = 30
	}
	var day, hour, minute int
	if _, err := fmt.Sscanf(clock, "第 %d 日 %d:%d", &day, &hour, &minute); err != nil || day < 1 || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return clock
	}
	total := (day-1)*24*60 + hour*60 + minute + minutes
	if total < 0 {
		return clock
	}
	return fmt.Sprintf("第 %d 日 %02d:%02d", total/(24*60)+1, (total/60)%24, total%60)
}

func sourceTypeFor(private bool, id, recipient string) string {
	if private && id != recipient {
		return "observed_private_conversation"
	}
	if private {
		return "direct_private_message"
	}
	return "direct_hearing"
}

func describeRecipient(def gameDefinition, recipient string) string {
	if recipient == "" {
		return "未明确指定具体人物"
	}
	if character, ok := characterByID(def, recipient); ok {
		return fmt.Sprintf("%s（%s）", character.Name, character.Role)
	}
	return "未明确指定具体人物"
}

func (a *App) decideNPCs(ctx context.Context, generator model.TextGenerator, snapshot worldSnapshot, def gameDefinition, recipient, intentType string, perceptions, priorTurn map[string]string, decisions map[string]npcDecision, stage int, stimulus string) error {
	if generator == nil {
		return ErrModelNotConfigured
	}
	npcCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var decisionMu sync.Mutex
	for _, character := range sceneCharacters(snapshot.Characters) {
		character := character
		perception, present := perceptions[character.EntityID]
		if stage > 1 && !present {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			input := buildNPCPrompt(snapshot, def, character, recipient, intentType, perception, priorTurn[character.EntityID], stage, stimulus)
			var decision npcDecision
			callCtx, callCancel := context.WithTimeout(npcCtx, 60*time.Second)
			defer callCancel()
			err := generateJSONWithNullableFields(callCtx, generator, "你是一个重要 NPC。只根据自己的角色资料、个人记忆和本阶段感知作决定。你可以沉默；speech 是你愿意让在场者听见的对白，action_intent 只是尝试，不是已经发生的事实。memory 只写本次真正获知的简短经历。", input, &decision, structuredTurnOutputTokens, []string{"speech", "action_intent", "memory"}, "speech", "action_intent", "silent", "memory")
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				errMu.Unlock()
				return
			}
			decision.Speech = cleanText(decision.Speech)
			decision.ActionIntent = cleanText(decision.ActionIntent)
			decision.Memory = cleanText(decision.Memory)
			if decision.Speech == "" {
				decision.Silent = true
			}
			if decision.Silent {
				decision.Speech = ""
			}
			decisionMu.Lock()
			decisions[character.EntityID] = decision
			decisionMu.Unlock()
		}()
	}
	wg.Wait()
	return firstErr
}

func buildNPCPrompt(snapshot worldSnapshot, def gameDefinition, character Character, recipient, intentType, perception, priorTurn string, stage int, stimulus string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "世界：%s；地点：%s；时间：%s；阶段：%d；玩家意图类型：%s\n", GameID, snapshot.Summary.Scene, snapshot.Summary.Clock, stage, intentType)
	fmt.Fprintf(&builder, "你的身份：%s（%s）\n角色资料：%s\n你知道的初始背景：%s\n", character.Name, character.Role, character.Profile, character.Knowledge)
	if recipient == "" {
		builder.WriteString("玩家本轮没有明确指定具体对象。请根据自己的感知决定是否回应。\n")
	} else if recipient == character.EntityID {
		fmt.Fprintf(&builder, "玩家本轮明确对你说话，目标是%s。你是直接回应者，请优先决定你对玩家的自然回应。\n", describeRecipient(def, recipient))
	} else {
		fmt.Fprintf(&builder, "玩家本轮明确对%s说话。你不是直接回应者，不要代替目标人物回答；只有在有自然理由时才公开反应，否则保持沉默。\n", describeRecipient(def, recipient))
	}
	fmt.Fprintf(&builder, "你的近期个人感知（来源和发言者必须保持一致）：\n%s\n", joinPerceptions(snapshot, snapshot.Perceptions[character.EntityID]))
	fmt.Fprintf(&builder, "你的个人经历：\n%s\n", joinMemories(snapshot.Memories[character.EntityID]))
	if priorTurn != "" {
		fmt.Fprintf(&builder, "本轮此前只有你自己知道的感知与决定：\n%s\n", priorTurn)
	}
	fmt.Fprintf(&builder, "本阶段新感知：%s\n", perception)
	if stimulus != "" {
		fmt.Fprintf(&builder, "新刺激：%s\n", stimulus)
	}
	fmt.Fprintf(&builder, "玩家本轮在你可见范围内的表达：%s\n输出 JSON：speech、action_intent、silent、memory。不要输出额外字段。", perception)
	return builder.String()
}

func formatSelfDecision(decision npcDecision) string {
	return fmt.Sprintf("speech=%q；action_intent=%q；silent=%t；memory=%q", decision.Speech, decision.ActionIntent, decision.Silent, decision.Memory)
}

func joinPerceptions(snapshot worldSnapshot, items []Perception) string {
	var parts []string
	for _, item := range items {
		label := item.SourceType
		if event, ok := eventByID(snapshot.Events, item.SourceEventID); ok && event.ActorID != "" {
			label = fmt.Sprintf("%s(%s)", characterDisplayName(snapshot.Characters, event.ActorID), item.SourceType)
		}
		parts = append(parts, fmt.Sprintf("[%s] %s", label, item.Content))
	}
	if len(parts) == 0 {
		return "（暂无）"
	}
	return strings.Join(parts, "\n")
}

func eventByID(events []Event, id string) (Event, bool) {
	for _, event := range events {
		if event.EventID == id {
			return event, true
		}
	}
	return Event{}, false
}

func characterDisplayName(characters []Character, id string) string {
	for _, character := range characters {
		if character.EntityID == id {
			return fmt.Sprintf("%s（%s）", character.Name, character.Role)
		}
	}
	if id == "player" {
		return "玩家"
	}
	return id
}

func joinMemories(items []Memory) string {
	var parts []string
	for _, item := range items {
		parts = append(parts, item.Kind+"："+item.Content)
	}
	if len(parts) == 0 {
		return "（暂无）"
	}
	return strings.Join(parts, "\n")
}

func (a *App) coordinateTurn(ctx context.Context, generator model.TextGenerator, snapshot worldSnapshot, run Run, intent turnIntent, decisions map[string]npcDecision, events []Event, publicReplies string) (hostResult, error) {
	if generator == nil {
		return hostResult{}, ErrModelNotConfigured
	}
	var actionCandidates []Event
	for _, event := range events {
		if event.EventType == "npc_action_intent" {
			actionCandidates = append(actionCandidates, event)
		}
	}
	actionJSON, _ := json.Marshal(actionCandidates)
	input := fmt.Sprintf("世界：%s\n当前地点：%s\n当前时间：%s\n玩家本轮输入：%s\n结构化意图：type=%s；target=%s；visibility=%s\nNPC 已确定的公开对白：%s\nNPC 协调提案（只包含公开对白、行动尝试与沉默状态，不含个人记忆）：\n%s\n待裁定行动(JSON)：%s\n所有可用重要人物：%s\n当前在场人物 entity_id：%s\n请协调本轮事实。每个待裁定行动必须且只能产生一个 outcome，并用 action_id 精确引用；status 只能是 succeeded、failed、partial；content 写已确定结果而不是尝试；recipients 只列实际感知结果的 player 或人物 entity_id，行动者本人可省略。scene_characters 只给出回合结束后实际在场的重要 NPC entity_id，不要包含 player；人物进入或离开只影响之后的阶段，不回填此前信息。输出 JSON：time_minutes、scene、scene_characters、outcomes。", GameID, snapshot.Summary.Scene, snapshot.Summary.Clock, run.Input, intent.IntentType, intent.AddresseeID, intent.Visibility, publicReplies, coordinationDecisionContext(decisions, snapshot.Characters), actionJSON, availableCharacterIDs(snapshot.Characters), strings.Join(characterIDs(sceneCharacters(snapshot.Characters)), ","))
	var result hostResult
	callCtx, callCancel := context.WithTimeout(ctx, 60*time.Second)
	defer callCancel()
	if err := generateJSON(callCtx, generator, "你是场景协调 Agent。你可以读取本轮协调资料来裁定行动结果、时间和场景，但不要写玩家正文，也不要把 NPC 的行动尝试直接当成成功事实。", input, &result, structuredTurnOutputTokens, "time_minutes", "scene", "scene_characters", "outcomes"); err != nil {
		return hostResult{}, err
	}
	result.Scene = cleanText(result.Scene)
	if result.Scene == "" || result.TimeMinutes < 0 || result.TimeMinutes > 120 || result.SceneCharacters == nil || result.Outcomes == nil {
		return hostResult{}, fmt.Errorf("%w: invalid scene coordination fields", ErrGenerationFailed)
	}
	result.SceneCharacters = normalizeSceneCharacters(result.SceneCharacters)
	if err := validateSceneCharacters(result.SceneCharacters, snapshot.Characters); err != nil {
		return hostResult{}, err
	}
	return result, nil
}

func (a *App) narrateVisible(ctx context.Context, generator model.TextGenerator, snapshot worldSnapshot, run Run, def gameDefinition, recipient, intentType, playerProjection string, private bool, clock, scene string, sceneCharacters []string) (narrativeResult, error) {
	if generator == nil {
		return narrativeResult{}, ErrModelNotConfigured
	}
	playerInput := run.Input
	if private {
		playerInput = "玩家进行了私下交谈；耳语原文不属于玩家可见叙述输入。"
	}
	input := fmt.Sprintf("剧本：%s\n地点：%s\n时间：%s\n主角：%s\n主角简介：%s\n近期公开叙事：%s\n玩家本次可公开描述的表达：%s\n玩家意图类型：%s\n明确交谈对象：%s\n本轮玩家可见且已经确定的对白与结果：%s\n回合结束后在场人物：%s\n只根据以上玩家可见投影组织一段自然正文，不新增行动成功、秘密、承诺或人物立场。", GameID, scene, clock, snapshot.PlayerName, snapshot.PlayerProfile, narrativeHistory(snapshot.Messages), playerInput, intentType, describeRecipient(def, recipient), playerProjection, strings.Join(sceneCharacters, ","))
	callCtx, callCancel := context.WithTimeout(ctx, 60*time.Second)
	defer callCancel()
	narrative, err := generateNarrativeText(callCtx, generator, "你是玩家正文 Agent。你只能把已经投影给玩家的结果写成连贯叙事，不接触或猜测 NPC 私人记忆和隐藏结果。只输出故事正文，不要输出 JSON、代码块、标题或解释。", input)
	if err != nil {
		return narrativeResult{}, err
	}
	return narrativeResult{Narrative: narrative}, nil
}

func generateNarrativeText(ctx context.Context, generator model.TextGenerator, system, input string) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		requestSystem := system
		if attempt > 0 {
			requestSystem += "\n上一次响应不可用。请重新生成，只输出一段完整的故事正文。"
		}
		response, err := generator.GenerateText(ctx, model.TextRequest{System: requestSystem, Input: input, MaxInputTokens: 12000, MaxOutputTokens: structuredTurnOutputTokens, MaxResponseBytes: 1 << 20})
		if err != nil {
			if attempt == 0 && errors.Is(err, model.ErrInvalidTextResponse) {
				continue
			}
			return "", err
		}
		narrative, err := parseNarrativeText(response.Text)
		if err != nil {
			if attempt == 0 {
				continue
			}
			return "", err
		}
		return narrative, nil
	}
	return "", ErrGenerationFailed
}

func parseNarrativeText(text string) (string, error) {
	text = cleanText(text)
	if text == "" {
		return "", fmt.Errorf("%w: narrative is empty", ErrGenerationFailed)
	}
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) < 3 || !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			return "", fmt.Errorf("%w: narrative has an incomplete code fence", ErrGenerationFailed)
		}
		text = cleanText(strings.Join(lines[1:len(lines)-1], "\n"))
	}
	if strings.HasPrefix(text, "{") {
		if err := validateStrictJSON([]byte(text)); err != nil {
			return "", fmt.Errorf("%w: narrative JSON is invalid: %v", ErrGenerationFailed, err)
		}
		var legacy narrativeResult
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&legacy); err != nil {
			return "", fmt.Errorf("%w: narrative JSON does not match the legacy wrapper", ErrGenerationFailed)
		}
		text = cleanText(legacy.Narrative)
	}
	if text == "" {
		return "", fmt.Errorf("%w: narrative is empty", ErrGenerationFailed)
	}
	return text, nil
}

func narrativeHistory(messages []Message) string {
	var parts []string
	for i := len(messages) - 1; i >= 0 && len(parts) < 8; i-- {
		message := messages[i]
		if message.Kind == "narrative" && cleanText(message.Content) != "" {
			parts = append(parts, message.Content)
		}
	}
	if len(parts) == 0 {
		return "（暂无）"
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return strings.Join(parts, "\n")
}

func coordinationDecisionContext(decisions map[string]npcDecision, characters []Character) string {
	var parts []string
	for _, character := range sceneCharacters(characters) {
		decision := decisions[character.EntityID]
		parts = append(parts, fmt.Sprintf("%s（%s，%s）：speech=%q；action_intent=%q；silent=%t", character.Name, character.Role, character.EntityID, decision.Speech, decision.ActionIntent, decision.Silent))
	}
	if len(parts) == 0 {
		return "（暂无）"
	}
	return strings.Join(parts, "\n")
}

func characterIDs(items []Character) []string {
	result := make([]string, 0, len(items))
	for _, character := range items {
		result = append(result, character.EntityID)
	}
	return result
}

func availableCharacterIDs(items []Character) string {
	var ids []string
	for _, character := range items {
		ids = append(ids, fmt.Sprintf("%s=%s（%s）", character.EntityID, character.Name, character.Role))
	}
	return strings.Join(ids, "、")
}

func validateSceneCharacters(ids []string, characters []Character) error {
	available := make(map[string]bool, len(characters))
	for _, character := range characters {
		available[character.EntityID] = true
	}
	seen := make(map[string]bool, len(ids))
	for index, id := range ids {
		id = cleanText(id)
		if id == "" || !available[id] || seen[id] {
			return fmt.Errorf("%w: invalid scene character %q at index %d", ErrGenerationFailed, id, index)
		}
		ids[index] = id
		seen[id] = true
	}
	return nil
}

func normalizeSceneCharacters(ids []string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = cleanText(id)
		if id != "player" {
			result = append(result, id)
		}
	}
	return result
}

func generateJSON(ctx context.Context, generator model.TextGenerator, system, input string, target any, maxOutput int, requiredFields ...string) error {
	return generateJSONWithNullableFields(ctx, generator, system, input, target, maxOutput, nil, requiredFields...)
}

func generateJSONWithNullableFields(ctx context.Context, generator model.TextGenerator, system, input string, target any, maxOutput int, nullableFields []string, requiredFields ...string) error {
	for attempt := 0; attempt < 2; attempt++ {
		requestSystem := system
		if attempt > 0 {
			requestSystem += "\n上一次响应不是可接受的完整 JSON。请重新生成，只输出满足字段要求的单个 JSON 对象。"
		}
		response, err := generator.GenerateText(ctx, model.TextRequest{System: requestSystem, Input: input, MaxInputTokens: 12000, MaxOutputTokens: maxOutput, MaxResponseBytes: 1 << 20})
		if err != nil {
			if attempt == 0 && errors.Is(err, model.ErrInvalidTextResponse) {
				continue
			}
			return err
		}
		if err := decodeGeneratedJSON(response.Text, target, nullableFields, requiredFields); err != nil {
			if attempt == 0 {
				continue
			}
			return err
		}
		return nil
	}
	return ErrGenerationFailed
}

func decodeGeneratedJSON(text string, target any, nullableFields, requiredFields []string) error {
	if err := validateStrictJSON([]byte(text)); err != nil {
		return err
	}
	if len(requiredFields) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &object); err != nil || object == nil {
			return ErrGenerationFailed
		}
		nullable := make(map[string]bool, len(nullableFields))
		for _, field := range nullableFields {
			nullable[field] = true
		}
		for _, field := range requiredFields {
			raw, ok := object[field]
			if !ok {
				return fmt.Errorf("%w: required field %q is missing", ErrGenerationFailed, field)
			}
			if strings.EqualFold(strings.TrimSpace(string(raw)), "null") && !nullable[field] {
				return fmt.Errorf("%w: required field %q is null", ErrGenerationFailed, field)
			}
		}
	}
	value := reflect.ValueOf(target)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		value.Elem().Set(reflect.Zero(value.Elem().Type()))
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}
