package storyapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Narrative   string `json:"narrative"`
	TimeMinutes int    `json:"time_minutes"`
	Scene       string `json:"scene"`
}

type turnOutput struct {
	Narrative    string
	Clock        string
	Scene        string
	SceneVersion int64
	Events       []Event
	Perceptions  []Perception
	Memories     []Memory
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
	if request.ExpectedMessageHead > 0 && request.ExpectedMessageHead != snapshot.Summary.MessageHead {
		return Run{}, ErrVersionConflict
	}
	if request.ExpectedEventHead > 0 && request.ExpectedEventHead != snapshot.Summary.EventHead {
		return Run{}, ErrVersionConflict
	}
	if request.ExpectedContextEpoch > 0 && request.ExpectedContextEpoch != snapshot.Summary.ContextEpoch {
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
	run := Run{
		RunID: newID("run"), RequestKey: request.RequestKey, RequestHash: a.hashRun(request),
		Input: request.Input, AddresseeID: cleanText(request.AddresseeID), Attempt: attempt,
		Status: "accepted", CreatedAt: now, UpdatedAt: now,
		BaseTurnSeq: snapshot.Summary.TurnSeq, BaseMessageHead: snapshot.Summary.MessageHead,
		BaseEventHead: snapshot.Summary.EventHead, BaseContextEpoch: snapshot.Summary.ContextEpoch,
		BaseSceneVersion: snapshot.SceneVersion,
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO runs(
		run_id,request_key,request_hash,input,addressee_id,attempt,status,base_turn_seq,base_message_head,base_event_head,base_context_epoch,base_scene_version,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.RunID, run.RequestKey, run.RequestHash, run.Input,
		run.AddresseeID, run.Attempt, run.Status, run.BaseTurnSeq, run.BaseMessageHead, run.BaseEventHead,
		run.BaseContextEpoch, run.BaseSceneVersion, run.CreatedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
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
		return
	}
	store, err := openWorldDB(path)
	if err != nil {
		return
	}
	defer store.db.Close()
	if err := updateRunStatus(context.Background(), store.db, run.RunID, "running", "", ""); err != nil {
		return
	}
	if !a.isActive(context.Background(), runtime.WorldID, runtime.ActiveRevision) {
		_ = updateRunStatus(context.Background(), store.db, run.RunID, "cancelled", "world_switched", "the active world changed")
		return
	}
	output, err := a.executeTurn(ctx, store, run, runtime.Generator)
	if err != nil {
		status, reason, message := "failed", "generation_failed", "the turn did not complete"
		if errors.Is(err, context.Canceled) {
			status, reason, message = "cancelled", "cancelled", "the turn was cancelled"
		} else if errors.Is(err, ErrVersionConflict) {
			reason, message = "version_conflict", "the world changed before this turn could be saved"
		}
		_ = updateRunStatus(context.Background(), store.db, run.RunID, status, reason, message)
		return
	}
	if !a.isActive(context.Background(), runtime.WorldID, runtime.ActiveRevision) {
		_ = updateRunStatus(context.Background(), store.db, run.RunID, "cancelled", "world_switched", "the active world changed")
		return
	}
	if _, err := commitTurn(ctx, store, run, output.Narrative, output.Events, output.Perceptions, output.Memories, output.Clock, output.Scene, output.SceneVersion); err != nil {
		status, reason, message := "failed", "storage_unavailable", "the completed turn could not be saved"
		if errors.Is(err, context.Canceled) {
			status, reason, message = "cancelled", "cancelled", "the turn was cancelled"
		} else if errors.Is(err, ErrVersionConflict) {
			reason, message = "version_conflict", "the world changed before this turn could be saved"
		}
		_ = updateRunStatus(context.Background(), store.db, run.RunID, status, reason, message)
		return
	}
	_ = a.touchWorld(context.Background(), runtime.WorldID)
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
	rows, err := store.db.QueryContext(ctx, `SELECT run_id,request_key,request_hash,input,addressee_id,attempt,status,reason,error,message_seq,base_turn_seq,base_message_head,base_event_head,base_context_epoch,base_scene_version,created_at,updated_at FROM runs ORDER BY created_at DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
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
	if strings.TrimSpace(requestKey) == "" {
		requestKey = newID("retry")
	}
	return a.SubmitRun(ctx, worldID, RunRequest{
		RequestKey: requestKey, Input: run.Input, AddresseeID: run.AddresseeID, attempt: run.Attempt + 1,
		ExpectedMessageHead: run.BaseMessageHead, ExpectedEventHead: run.BaseEventHead,
		ExpectedContextEpoch: run.BaseContextEpoch,
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
	input := fmt.Sprintf("当前地点：%s\n当前时间：%s\n在场人物：\n%s玩家输入：%s\n显式目标（若有）：%s\n请判断玩家本轮是 speak、observe 还是 act；如果玩家明确向某个在场人物说话，只返回该人物的 entity_id。visibility 只能是 public 或 private。只输出 JSON：{\"intent_type\":\"speak\",\"addressee_id\":\"npc:...\",\"visibility\":\"public\"}。人物名出现在谈话内容里不等于玩家正在对该人物说话。", snapshot.Summary.Scene, snapshot.Summary.Clock, characters.String(), run.Input, explicitRecipient)
	var intent turnIntent
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := generateJSON(callCtx, generator, "你负责把玩家本轮输入解析成结构化回合意图。只根据输入和在场名单判断目标、可见范围与意图类型，不替玩家执行行动。", input, &intent, 512, "intent_type", "addressee_id", "visibility"); err != nil {
		return turnIntent{}, err
	}
	intent.IntentType = strings.ToLower(cleanText(intent.IntentType))
	intent.Visibility = strings.ToLower(cleanText(intent.Visibility))
	intent.AddresseeID = cleanText(intent.AddresseeID)
	if explicitRecipient != "" {
		intent.AddresseeID = explicitRecipient
	} else if hintedRecipient := defaultAddressee(run.Input); hintedRecipient != "" {
		intent.AddresseeID = hintedRecipient
	}
	if intent.IntentType == "" {
		return turnIntent{}, ErrGenerationFailed
	}
	if intent.IntentType != "speak" && intent.IntentType != "observe" && intent.IntentType != "act" {
		return turnIntent{}, ErrGenerationFailed
	}
	if intent.AddresseeID != "" {
		if _, ok := findSceneCharacter(participants, intent.AddresseeID); !ok {
			return turnIntent{}, ErrInvalidRequest
		}
	}
	if intent.Visibility == "" {
		if privateInputHint(run.Input, intent.AddresseeID, participants) {
			intent.Visibility = "private"
		} else {
			intent.Visibility = "public"
		}
	}
	if intent.Visibility != "public" && intent.Visibility != "private" {
		return turnIntent{}, ErrGenerationFailed
	}
	if privateInputHint(run.Input, intent.AddresseeID, participants) {
		intent.Visibility = "private"
	}
	if intent.Visibility == "private" && intent.AddresseeID == "" {
		return turnIntent{}, ErrInvalidRequest
	}
	return intent, nil
}

func (a *App) executeTurn(ctx context.Context, store *worldStore, run Run, generator model.TextGenerator) (turnOutput, error) {
	snapshot, err := loadWorldSnapshot(ctx, store, 40)
	if err != nil {
		return turnOutput{}, err
	}
	def := lanternDefinition()
	intent, err := resolveTurnIntent(ctx, generator, snapshot, run)
	if err != nil {
		return turnOutput{}, err
	}
	recipient := intent.AddresseeID
	private := intent.Visibility == "private"
	participants := sceneCharacters(snapshot.Characters)
	now := time.Now().UTC()
	playerEventID := run.RunID + ":input"
	output := turnOutput{
		Clock: snapshot.Summary.Clock, Scene: snapshot.Summary.Scene, SceneVersion: snapshot.SceneVersion,
		Events:   []Event{{EventID: playerEventID, EventType: "player_attempt", ActorID: "player", TargetID: recipient, Content: run.Input, RunID: run.RunID, Stage: 1, SceneVersion: snapshot.SceneVersion, SourceType: "player", CreatedAt: now}},
		Memories: []Memory{}, Perceptions: []Perception{},
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
	if err := a.decideNPCs(ctx, generator, snapshot, def, recipient, intent.IntentType, perceptText, decisions, 1, ""); err != nil {
		return turnOutput{}, err
	}
	var publicReplyLog []string
	var decisionHistory []string
	for _, character := range participants {
		decision := decisions[character.EntityID]
		decisionHistory = append(decisionHistory, formatDecision(character, decision, 1))
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
			if err := a.decideNPCs(ctx, generator, snapshot, def, recipient, intent.IntentType, followText, followDecisions, 2, follow); err != nil {
				return turnOutput{}, err
			}
			decision, ok := followDecisions[character.EntityID]
			if !ok {
				continue
			}
			decisionHistory = append(decisionHistory, formatDecision(character, decision, 2))
			if reply := appendNPCDecisionOutput(&output, run, character, decision, participants, followText[character.EntityID], playerEventID, snapshot.SceneVersion, 2); reply != "" {
				publicReplyLog = append(publicReplyLog, reply)
			}
			decisions[character.EntityID] = mergeNPCDecision(previous, decision)
		}
	}

	public := strings.Join(publicReplyLog, "\n")
	if public == "" {
		public = "没有人立刻回答。客栈里的雨声显得更清楚了。"
	}
	host, err := a.narrate(ctx, generator, snapshot, run, def, recipient, intent.IntentType, public, private, decisions, decisionHistory)
	if err != nil {
		return turnOutput{}, err
	}
	output.Narrative = host.Narrative
	output.Clock = advanceClock(snapshot.Summary.Clock, run.Input, host.TimeMinutes)
	if host.Scene != "" {
		output.Scene = host.Scene
	}
	if output.Scene != snapshot.Summary.Scene {
		output.SceneVersion = snapshot.SceneVersion + 1
	}
	output.Events = append(output.Events, Event{EventID: run.RunID + ":outcome", EventType: "turn_settled", ActorID: "scene", Content: public, RunID: run.RunID, Stage: 3, SceneVersion: output.SceneVersion, SourceType: "scene", CreatedAt: time.Now().UTC()})
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
	if decision.Silent {
		output.Perceptions = append(output.Perceptions, Perception{RecipientID: character.EntityID, SourceEventID: playerEventID, SourceType: "observation", Content: perception, Stage: stage, SceneVersion: sceneVersion, CreatedAt: time.Now().UTC()})
	}
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

func (a *App) decideNPCs(ctx context.Context, generator model.TextGenerator, snapshot worldSnapshot, def gameDefinition, recipient, intentType string, perceptions map[string]string, decisions map[string]npcDecision, stage int, stimulus string) error {
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
			input := buildNPCPrompt(snapshot, def, character, recipient, intentType, perception, stage, stimulus)
			var decision npcDecision
			callCtx, callCancel := context.WithTimeout(npcCtx, 60*time.Second)
			defer callCancel()
			err := generateJSON(callCtx, generator, "你是一个重要 NPC。只根据自己的角色资料、个人记忆和本阶段感知作决定。你可以沉默；speech 是你愿意让在场者听见的对白，action_intent 只是尝试，不是已经发生的事实。memory 只写本次真正获知的简短经历。", input, &decision, 1024, "speech", "action_intent", "silent", "memory")
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

func buildNPCPrompt(snapshot worldSnapshot, def gameDefinition, character Character, recipient, intentType, perception string, stage int, stimulus string) string {
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
	fmt.Fprintf(&builder, "本阶段新感知：%s\n", perception)
	if stimulus != "" {
		fmt.Fprintf(&builder, "新刺激：%s\n", stimulus)
	}
	fmt.Fprintf(&builder, "玩家本轮在你可见范围内的表达：%s\n输出 JSON：speech、action_intent、silent、memory。不要输出额外字段。", perception)
	return builder.String()
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

func (a *App) narrate(ctx context.Context, generator model.TextGenerator, snapshot worldSnapshot, run Run, def gameDefinition, recipient, intentType string, public string, private bool, decisions map[string]npcDecision, decisionHistory []string) (hostResult, error) {
	if generator == nil {
		return hostResult{}, ErrModelNotConfigured
	}
	playerInput := run.Input
	if private {
		playerInput = "玩家进行了私下交谈；耳语原文不提供给场景主，只能根据已经公开的回应组织叙事。"
	}
	input := fmt.Sprintf("剧本：%s\n地点：%s\n时间：%s\n主角：%s\n主角简介：%s\n近期公开叙事：%s\n玩家本次表达：%s\n玩家本轮意图类型：%s\n玩家本轮明确对谁说：%s\n人物已确定的公开回应（每条回应前的角色名就是实际发言者，不要把台词改分配给其他人物）：%s\n人物内部决策提案（仅供场景主协调；action_intent 是行动尝试，memory 是个人记忆，不得直接当成已经发生的公开事实）：%s\n本轮所有 NPC 决策记录：%s\n在场重要人物：%s\n请组织一段玩家可见的自然正文。耳语原文只可在授权人物的内部经历中使用，不能把未公开秘密写给玩家。玩家的输入和 NPC 的 action_intent 都是行动尝试，不是已经成功的事实。输出 JSON，字段 narrative、time_minutes、scene。time_minutes 只能是 0 到 120 的非负整数。", GameID, snapshot.Summary.Scene, snapshot.Summary.Clock, snapshot.PlayerName, snapshot.PlayerProfile, narrativeHistory(snapshot.Messages), playerInput, intentType, describeRecipient(def, recipient), public, decisionContext(decisions, snapshot.Characters), strings.Join(decisionHistory, "\n"), characterNames(sceneCharacters(snapshot.Characters)))
	var result hostResult
	callCtx, callCancel := context.WithTimeout(ctx, 60*time.Second)
	defer callCancel()
	if err := generateJSON(callCtx, generator, "你是场景主 Agent。你负责把已经确定的公开结果组织成连贯叙事，不替重要人物做未经决策的选择，不向玩家泄露作者秘密。", input, &result, 2048, "narrative", "time_minutes", "scene"); err != nil {
		return hostResult{}, err
	}
	result.Narrative = cleanText(result.Narrative)
	result.Scene = cleanText(result.Scene)
	if result.Narrative == "" || result.TimeMinutes < 0 || result.TimeMinutes > 120 {
		return hostResult{}, ErrGenerationFailed
	}
	return result, nil
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

func decisionContext(decisions map[string]npcDecision, characters []Character) string {
	var parts []string
	for _, character := range sceneCharacters(characters) {
		decision := decisions[character.EntityID]
		parts = append(parts, fmt.Sprintf("%s（%s）：speech=%q；action_intent=%q；silent=%t；memory=%q", character.Name, character.Role, decision.Speech, decision.ActionIntent, decision.Silent, decision.Memory))
	}
	if len(parts) == 0 {
		return "（暂无）"
	}
	return strings.Join(parts, "\n")
}

func formatDecision(character Character, decision npcDecision, stage int) string {
	return fmt.Sprintf("阶段%d %s（%s）：speech=%q；action_intent=%q；silent=%t；memory=%q", stage, character.Name, character.Role, decision.Speech, decision.ActionIntent, decision.Silent, decision.Memory)
}

func characterNames(items []Character) string {
	var names []string
	for _, character := range items {
		names = append(names, character.Name)
	}
	return strings.Join(names, "、")
}

func generateJSON(ctx context.Context, generator model.TextGenerator, system, input string, target any, maxOutput int, requiredFields ...string) error {
	response, err := generator.GenerateText(ctx, model.TextRequest{System: system, Input: input, MaxInputTokens: 12000, MaxOutputTokens: maxOutput, MaxResponseBytes: 1 << 20})
	if err != nil {
		return err
	}
	if err := validateStrictJSON([]byte(response.Text)); err != nil {
		return err
	}
	if len(requiredFields) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(response.Text), &object); err != nil || object == nil {
			return ErrGenerationFailed
		}
		for _, field := range requiredFields {
			if _, ok := object[field]; !ok {
				return ErrGenerationFailed
			}
		}
	}
	decoder := json.NewDecoder(strings.NewReader(response.Text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}
