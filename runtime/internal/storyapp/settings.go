package storyapp

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

const (
	PerspectiveFirstPerson  = "first_person"
	PerspectiveSecondPerson = "second_person"
	PerspectiveThirdPerson  = "third_person"

	NarrativeLengthConcise  = "concise"
	NarrativeLengthStandard = "standard"
	NarrativeLengthDetailed = "detailed"

	NarrativeDetailRestrained = "restrained"
	NarrativeDetailBalanced   = "balanced"
	NarrativeDetailRich       = "rich"
)

type NarrativeSettings struct {
	Perspective       string `json:"perspective"`
	Length            string `json:"length"`
	Detail            string `json:"detail"`
	CustomInstruction string `json:"custom_instruction"`
}

type UpdateNarrativeSettingsRequest struct {
	Perspective          string `json:"perspective"`
	Length               string `json:"length"`
	Detail               string `json:"detail"`
	CustomInstruction    string `json:"custom_instruction"`
	ExpectedContextEpoch int64  `json:"expected_context_epoch"`
}

func defaultNarrativeSettings() NarrativeSettings {
	return NarrativeSettings{
		Perspective: PerspectiveSecondPerson,
		Length:      NarrativeLengthStandard,
		Detail:      NarrativeDetailBalanced,
	}
}

func validateNarrativeSettings(settings NarrativeSettings) (NarrativeSettings, error) {
	settings.Perspective = strings.TrimSpace(settings.Perspective)
	settings.Length = strings.TrimSpace(settings.Length)
	settings.Detail = strings.TrimSpace(settings.Detail)
	settings.CustomInstruction = cleanText(settings.CustomInstruction)
	if settings.Perspective != PerspectiveFirstPerson && settings.Perspective != PerspectiveSecondPerson && settings.Perspective != PerspectiveThirdPerson {
		return NarrativeSettings{}, ErrInvalidRequest
	}
	if settings.Length != NarrativeLengthConcise && settings.Length != NarrativeLengthStandard && settings.Length != NarrativeLengthDetailed {
		return NarrativeSettings{}, ErrInvalidRequest
	}
	if settings.Detail != NarrativeDetailRestrained && settings.Detail != NarrativeDetailBalanced && settings.Detail != NarrativeDetailRich {
		return NarrativeSettings{}, ErrInvalidRequest
	}
	if len([]rune(settings.CustomInstruction)) > 1000 {
		return NarrativeSettings{}, ErrInvalidRequest
	}
	return settings, nil
}

func loadNarrativeSettings(ctx context.Context, db *sql.DB) NarrativeSettings {
	settings := defaultNarrativeSettings()
	for key, target := range map[string]*string{
		"narrative_perspective":        &settings.Perspective,
		"narrative_length":             &settings.Length,
		"narrative_detail":             &settings.Detail,
		"narrative_custom_instruction": &settings.CustomInstruction,
	} {
		if value, err := metaGet(ctx, db, key); err == nil {
			*target = value
		}
	}
	validated, err := validateNarrativeSettings(settings)
	if err != nil {
		return defaultNarrativeSettings()
	}
	return validated
}

func (a *App) UpdateNarrativeSettings(ctx context.Context, worldID string, request UpdateNarrativeSettingsRequest) (NarrativeSettings, WorldSummary, error) {
	settings, err := validateNarrativeSettings(NarrativeSettings{
		Perspective: request.Perspective, Length: request.Length, Detail: request.Detail,
		CustomInstruction: request.CustomInstruction,
	})
	if err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	}
	path, status, err := a.worldRecord(ctx, worldID)
	if err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	}
	if status != "ready" {
		return NarrativeSettings{}, WorldSummary{}, ErrWorldNotReady
	}
	world := a.worldRuntimeFor(worldID)
	world.mu.Lock()
	defer world.mu.Unlock()
	if world.savePending {
		return NarrativeSettings{}, WorldSummary{}, ErrWorldBusy
	}
	store, err := openWorldDB(path)
	if err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	}
	defer store.db.Close()
	if count, err := countActiveRuns(ctx, store.db); err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	} else if count > 0 {
		return NarrativeSettings{}, WorldSummary{}, ErrWorldBusy
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	}
	defer tx.Rollback()
	currentEpochText, err := metaGetTx(ctx, tx, "context_epoch")
	if err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	}
	currentEpoch, err := strconv.ParseInt(currentEpochText, 10, 64)
	if err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	}
	if request.ExpectedContextEpoch > 0 && request.ExpectedContextEpoch != currentEpoch {
		return NarrativeSettings{}, WorldSummary{}, ErrVersionConflict
	}
	values := map[string]string{
		"narrative_perspective":        settings.Perspective,
		"narrative_length":             settings.Length,
		"narrative_detail":             settings.Detail,
		"narrative_custom_instruction": settings.CustomInstruction,
		"context_epoch":                strconv.FormatInt(currentEpoch+1, 10),
		"updated_at":                   nowText(),
	}
	for key, value := range values {
		if err := metaSetTx(ctx, tx, key, value); err != nil {
			return NarrativeSettings{}, WorldSummary{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	}
	if err := a.touchWorld(ctx, worldID); err != nil {
		return NarrativeSettings{}, WorldSummary{}, err
	}
	summary, err := a.worldSummary(ctx, worldID)
	return settings, summary, err
}

func narrativePerspectiveInstruction(settings NarrativeSettings, playerName string) string {
	switch settings.Perspective {
	case PerspectiveFirstPerson:
		return "使用第一人称有限视角。正文旁白用“我”指代主角；NPC 对白中的“我”仍属于说话的 NPC，不能混淆说话人。"
	case PerspectiveThirdPerson:
		return "使用第三人称有限视角。正文旁白用主角姓名“" + playerName + "”指代主角，不用“玩家”“来人”“来客”或泛称“旅人”替代。"
	default:
		return "使用第二人称有限视角。正文旁白用“你”指代主角，不用“玩家”“来人”“来客”“旅人”或主角姓名作为第三人称代称。"
	}
}

func narrativeLengthInstruction(settings NarrativeSettings) (string, int) {
	switch settings.Length {
	case NarrativeLengthConcise:
		return "正文保持简短，通常为 120 至 300 个汉字，优先保留本轮变化与关键对白。", 768
	case NarrativeLengthDetailed:
		return "正文可以细致展开，通常为 600 至 1200 个汉字，但不得用重复状态或无意义的否定句填充篇幅。", 3072
	default:
		return "正文使用标准篇幅，通常为 300 至 600 个汉字，完整呈现本轮变化并保持节奏。", 1536
	}
}

func narrativeDetailInstruction(settings NarrativeSettings) string {
	switch settings.Detail {
	case NarrativeDetailRestrained:
		return "描写保持克制，只写理解本轮所需的动作、对白和环境变化。"
	case NarrativeDetailRich:
		return "可以增加较丰富的感官、环境和动作细节，但这些细节不得创造新事实或重复列举没有发生的变化。"
	default:
		return "使用平衡的描写密度，以清晰动作和对白为主，补充少量有作用的环境与感官细节。"
	}
}

func narrativeReference(settings NarrativeSettings, playerName string) string {
	switch settings.Perspective {
	case PerspectiveFirstPerson:
		return "我"
	case PerspectiveThirdPerson:
		return playerName
	default:
		return "你"
	}
}
