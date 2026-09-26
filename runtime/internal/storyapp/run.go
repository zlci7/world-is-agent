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

type hostResult struct {
	Narrative   string `json:"narrative"`
	TimeMinutes int    `json:"time_minutes"`
	Scene       string `json:"scene"`
}

type turnOutput struct {
	Narrative   string
	Clock       string
	Events      []Event
	Perceptions []Perception
	Memories    []Memory
}

func (a *App) SubmitRun(ctx context.Context, worldID string, request RunRequest) (Run, error) {
	request.Input = cleanText(request.Input)
	request.RequestKey = strings.TrimSpace(request.RequestKey)
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
	configured := a.generator != nil
	a.modelMu.RUnlock()
	if !configured {
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
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO runs(
		run_id,request_key,request_hash,input,addressee_id,attempt,status,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, run.RunID, run.RequestKey, run.RequestHash, run.Input,
		run.AddresseeID, run.Attempt, run.Status, run.CreatedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Run{}, err
	}
	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	runtime := &runRuntime{Cancel: cancel, Done: make(chan struct{}), WorldID: worldID, RunID: run.RunID, ActiveRevision: activeRevision}
	a.runsMu.Lock()
	a.runs[run.RunID] = runtime
	a.runsMu.Unlock()
	go a.runWorker(runCtx, runtime, run)
	return run, nil
}

func (a *App) runWorker(ctx context.Context, runtime *runRuntime, run Run) {
	world := a.worldRuntimeFor(runtime.WorldID)
	world.mu.Lock()
	defer world.mu.Unlock()
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
	output, err := a.executeTurn(ctx, store, run)
	if err != nil {
		status, reason, message := "failed", "generation_failed", "the turn did not complete"
		if errors.Is(err, context.Canceled) {
			status, reason, message = "cancelled", "cancelled", "the turn was cancelled"
		}
		_ = updateRunStatus(context.Background(), store.db, run.RunID, status, reason, message)
		return
	}
	if !a.isActive(context.Background(), runtime.WorldID, runtime.ActiveRevision) {
		_ = updateRunStatus(context.Background(), store.db, run.RunID, "cancelled", "world_switched", "the active world changed")
		return
	}
	if _, err := commitTurn(ctx, store, run, output.Narrative, output.Events, output.Perceptions, output.Memories, output.Clock); err != nil {
		status, reason, message := "failed", "storage_unavailable", "the completed turn could not be saved"
		if errors.Is(err, context.Canceled) {
			status, reason, message = "cancelled", "cancelled", "the turn was cancelled"
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
	return a.SubmitRun(ctx, worldID, RunRequest{RequestKey: requestKey, Input: run.Input, AddresseeID: run.AddresseeID, attempt: run.Attempt + 1})
}

func (a *App) executeTurn(ctx context.Context, store *worldStore, run Run) (turnOutput, error) {
	snapshot, err := loadWorldSnapshot(ctx, store, 40)
	if err != nil {
		return turnOutput{}, err
	}
	def := lanternDefinition()
	recipient := run.AddresseeID
	if recipient == "" {
		recipient = defaultAddressee(run.Input)
	}
	private := isPrivateInput(run.Input) && recipient != ""
	now := time.Now().UTC()
	playerEventID := run.RunID + ":input"
	output := turnOutput{
		Clock:    snapshot.Summary.Clock,
		Events:   []Event{{EventID: playerEventID, EventType: "player_attempt", ActorID: "player", TargetID: recipient, Content: run.Input, RunID: run.RunID, Stage: 1, SceneVersion: snapshot.SceneVersion, SourceType: "player", CreatedAt: now}},
		Memories: []Memory{}, Perceptions: []Perception{},
	}
	decisions := make(map[string]npcDecision)
	perceptText := make(map[string]string)
	for _, character := range snapshot.Characters {
		if private && character.EntityID != recipient {
			perceptText[character.EntityID] = fmt.Sprintf("你看见玩家与%s低声交谈，但听不清内容。不要猜测耳语原文。", describeRecipient(def, recipient))
		} else {
			perceptText[character.EntityID] = run.Input
		}
		output.Perceptions = append(output.Perceptions, Perception{RecipientID: character.EntityID, SourceEventID: playerEventID, SourceType: sourceTypeFor(private, character.EntityID, recipient), Content: perceptText[character.EntityID], Stage: 1, SceneVersion: snapshot.SceneVersion, CreatedAt: now})
	}
	if err := a.decideNPCs(ctx, snapshot, def, recipient, perceptText, decisions, 1, ""); err != nil {
		return turnOutput{}, err
	}
	for _, character := range snapshot.Characters {
		decision := decisions[character.EntityID]
		if decision.Silent {
			output.Perceptions = append(output.Perceptions, Perception{RecipientID: character.EntityID, SourceEventID: playerEventID, SourceType: "observation", Content: perceptText[character.EntityID], Stage: 1, SceneVersion: snapshot.SceneVersion, CreatedAt: now})
		}
		if decision.Speech == "" {
			continue
		}
		eventID := run.RunID + ":" + character.EntityID + ":speech:1"
		output.Events = append(output.Events, Event{EventID: eventID, EventType: "npc_dialogue", ActorID: character.EntityID, TargetID: "player", Content: decision.Speech, RunID: run.RunID, Stage: 1, SceneVersion: snapshot.SceneVersion, SourceType: "visible_dialogue", CreatedAt: time.Now().UTC()})
		for _, other := range snapshot.Characters {
			if other.EntityID != character.EntityID {
				output.Perceptions = append(output.Perceptions, Perception{RecipientID: other.EntityID, SourceEventID: eventID, SourceType: "heard_public_reply", Content: decision.Speech, Stage: 1, SceneVersion: snapshot.SceneVersion, CreatedAt: time.Now().UTC()})
			}
		}
		if decision.Memory != "" {
			output.Memories = append(output.Memories, Memory{RecipientID: character.EntityID, Kind: "character_judgment", Content: cleanText(decision.Memory), SourceEventID: eventID, CreatedAt: time.Now().UTC()})
		}
	}

	// A public answer is a new stimulus. A quiet NPC gets one bounded follow-up chance.
	for _, character := range snapshot.Characters {
		if !decisions[character.EntityID].Silent || !hasSpeech(decisions) {
			continue
		}
		follow := publicReplies(decisions, snapshot.Characters)
		followDecisions := make(map[string]npcDecision)
		followText := map[string]string{character.EntityID: "另一位人物刚才公开回应：" + follow}
		if err := a.decideNPCs(ctx, snapshot, def, recipient, followText, followDecisions, 2, follow); err != nil {
			return turnOutput{}, err
		}
		decision := followDecisions[character.EntityID]
		decisions[character.EntityID] = decision
		if decision.Speech == "" {
			continue
		}
		eventID := run.RunID + ":" + character.EntityID + ":speech:2"
		output.Events = append(output.Events, Event{EventID: eventID, EventType: "npc_dialogue", ActorID: character.EntityID, TargetID: "player", Content: decision.Speech, RunID: run.RunID, Stage: 2, SceneVersion: snapshot.SceneVersion, SourceType: "visible_dialogue", CreatedAt: time.Now().UTC()})
		for _, other := range snapshot.Characters {
			if other.EntityID != character.EntityID {
				output.Perceptions = append(output.Perceptions, Perception{RecipientID: other.EntityID, SourceEventID: eventID, SourceType: "heard_public_reply", Content: decision.Speech, Stage: 2, SceneVersion: snapshot.SceneVersion, CreatedAt: time.Now().UTC()})
			}
		}
		if decision.Memory != "" {
			output.Memories = append(output.Memories, Memory{RecipientID: character.EntityID, Kind: "character_judgment", Content: cleanText(decision.Memory), SourceEventID: eventID, CreatedAt: time.Now().UTC()})
		}
	}

	public := publicReplies(decisions, snapshot.Characters)
	if public == "" {
		public = "没有人立刻回答。客栈里的雨声显得更清楚了。"
	}
	host, err := a.narrate(ctx, snapshot, run, def, recipient, public, private)
	if err != nil {
		return turnOutput{}, err
	}
	output.Narrative = host.Narrative
	output.Clock = advanceClock(snapshot.Summary.Clock, run.Input, host.TimeMinutes)
	output.Events = append(output.Events, Event{EventID: run.RunID + ":outcome", EventType: "turn_settled", ActorID: "scene", Content: public, RunID: run.RunID, Stage: 3, SceneVersion: snapshot.SceneVersion, SourceType: "scene", CreatedAt: time.Now().UTC()})
	for _, character := range snapshot.Characters {
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

func hasSpeech(decisions map[string]npcDecision) bool {
	for _, decision := range decisions {
		if decision.Speech != "" {
			return true
		}
	}
	return false
}

func publicReplies(decisions map[string]npcDecision, characters []Character) string {
	var parts []string
	for _, character := range characters {
		decision := decisions[character.EntityID]
		if decision.Speech != "" {
			parts = append(parts, fmt.Sprintf("%s（%s）说：%s", character.Name, character.Role, decision.Speech))
		}
	}
	return strings.Join(parts, "\n")
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

func (a *App) decideNPCs(ctx context.Context, snapshot worldSnapshot, def gameDefinition, recipient string, perceptions map[string]string, decisions map[string]npcDecision, stage int, stimulus string) error {
	a.modelMu.RLock()
	generator := a.generator
	a.modelMu.RUnlock()
	if generator == nil {
		return ErrModelNotConfigured
	}
	npcCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var decisionMu sync.Mutex
	for _, character := range snapshot.Characters {
		character := character
		perception, present := perceptions[character.EntityID]
		if stage > 1 && !present {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			input := buildNPCPrompt(snapshot, def, character, recipient, perception, stage, stimulus)
			var decision npcDecision
			callCtx, callCancel := context.WithTimeout(npcCtx, 60*time.Second)
			defer callCancel()
			err := generateJSON(callCtx, generator, "你是一个重要 NPC。只根据自己的角色资料、个人记忆和本阶段感知作决定。你可以沉默；speech 是你愿意让在场者听见的对白，action_intent 只是尝试，不是已经发生的事实。memory 只写本次真正获知的简短经历。", input, &decision, 1024)
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

func buildNPCPrompt(snapshot worldSnapshot, def gameDefinition, character Character, recipient string, perception string, stage int, stimulus string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "世界：%s；地点：%s；时间：%s；阶段：%d\n", GameID, snapshot.Summary.Scene, snapshot.Summary.Clock, stage)
	fmt.Fprintf(&builder, "你的身份：%s（%s）\n角色资料：%s\n你知道的初始背景：%s\n", character.Name, character.Role, character.Profile, character.Knowledge)
	if recipient == "" {
		builder.WriteString("玩家本轮没有明确指定具体对象。请根据自己的感知决定是否回应。\n")
	} else if recipient == character.EntityID {
		fmt.Fprintf(&builder, "玩家本轮明确对你说话，目标是%s。你是直接回应者，请优先决定你对玩家的自然回应。\n", describeRecipient(def, recipient))
	} else {
		fmt.Fprintf(&builder, "玩家本轮明确对%s说话。你不是直接回应者，不要代替目标人物回答；只有在有自然理由时才公开反应，否则保持沉默。\n", describeRecipient(def, recipient))
	}
	fmt.Fprintf(&builder, "你的近期个人感知：\n%s\n", joinPerceptions(snapshot.Perceptions[character.EntityID]))
	fmt.Fprintf(&builder, "你的个人经历：\n%s\n", joinMemories(snapshot.Memories[character.EntityID]))
	fmt.Fprintf(&builder, "本阶段新感知：%s\n", perception)
	if stimulus != "" {
		fmt.Fprintf(&builder, "新刺激：%s\n", stimulus)
	}
	fmt.Fprintf(&builder, "玩家本轮在你可见范围内的表达：%s\n输出 JSON：speech、action_intent、silent、memory。不要输出额外字段。", perception)
	return builder.String()
}

func joinPerceptions(items []Perception) string {
	var parts []string
	for _, item := range items {
		parts = append(parts, item.Content)
	}
	if len(parts) == 0 {
		return "（暂无）"
	}
	return strings.Join(parts, "\n")
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

func (a *App) narrate(ctx context.Context, snapshot worldSnapshot, run Run, def gameDefinition, recipient string, public string, private bool) (hostResult, error) {
	a.modelMu.RLock()
	generator := a.generator
	a.modelMu.RUnlock()
	if generator == nil {
		return hostResult{}, ErrModelNotConfigured
	}
	playerInput := run.Input
	if private {
		playerInput = "玩家进行了私下交谈；耳语原文不提供给场景主，只能根据已经公开的回应组织叙事。"
	}
	input := fmt.Sprintf("剧本：%s\n地点：%s\n时间：%s\n主角：%s\n玩家本次表达：%s\n玩家本轮明确对谁说：%s\n人物已确定的公开回应（每条回应前的角色名就是实际发言者，不要把台词改分配给其他人物）：%s\n在场重要人物：%s\n请组织一段玩家可见的自然正文。耳语原文只可在授权人物的内部经历中使用，不能把未公开秘密写给玩家。玩家的输入是行动尝试，不是已经成功的事实。输出 JSON，字段 narrative、time_minutes、scene。time_minutes 只能是 0 到 120 的非负整数。", GameID, snapshot.Summary.Scene, snapshot.Summary.Clock, snapshot.PlayerName, playerInput, describeRecipient(def, recipient), public, characterNames(snapshot.Characters))
	var result hostResult
	callCtx, callCancel := context.WithTimeout(ctx, 60*time.Second)
	defer callCancel()
	if err := generateJSON(callCtx, generator, "你是场景主 Agent。你负责把已经确定的公开结果组织成连贯叙事，不替重要人物做未经决策的选择，不向玩家泄露作者秘密。", input, &result, 2048); err != nil {
		return hostResult{}, err
	}
	result.Narrative = cleanText(result.Narrative)
	result.Scene = cleanText(result.Scene)
	if result.Narrative == "" || result.TimeMinutes < 0 || result.TimeMinutes > 120 {
		return hostResult{}, ErrGenerationFailed
	}
	return result, nil
}

func characterNames(items []Character) string {
	var names []string
	for _, character := range items {
		names = append(names, character.Name)
	}
	return strings.Join(names, "、")
}

func generateJSON(ctx context.Context, generator model.TextGenerator, system, input string, target any, maxOutput int) error {
	response, err := generator.GenerateText(ctx, model.TextRequest{System: system, Input: input, MaxInputTokens: 12000, MaxOutputTokens: maxOutput, MaxResponseBytes: 1 << 20})
	if err != nil {
		return err
	}
	if err := validateStrictJSON([]byte(response.Text)); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(response.Text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}
