package storyapp

import "strings"

type gameDefinition struct {
	Summary    GameSummary
	Opening    string
	Scene      string
	Clock      string
	Characters []Character
	Bystanders []string
	Secret     string
}

func lanternDefinition() gameDefinition {
	return gameDefinition{
		Summary: GameSummary{
			ID:          GameID,
			Title:       "暮灯镇的失踪信使",
			Description: "一场小型调查冒险：雨夜的旧渡口客栈里，失踪的信使留下了一封没有寄出的信。",
			Modes:       []string{"open", "guided"},
			DefaultMode: "guided",
		},
		Opening: "雨水顺着旧渡口客栈的屋檐落下。壁炉旁的老板沈岚擦拭着一只空酒杯，佣兵铁杉坐在窗边，目光落在黑沉沉的河面。门口还挤着十来个避雨的客人。就在你推门时，柜台下传来一声短促的金属碰撞。",
		Scene:   "旧渡口客栈",
		Clock:   "第 1 日 19:00",
		Secret:  "沈岚在柜台下藏着一枚染血的信蜡，知道失踪信使曾在今晚来过；铁杉只注意到有人和沈岚低声交谈过，不知道谈话内容。",
		Characters: []Character{
			{EntityID: "npc:innkeeper", DefinitionID: "innkeeper.v1", Name: "沈岚", Role: "客栈老板", Profile: "谨慎、善于观察，不愿让客人恐慌。她熟悉旧渡口的每一条消息，遇到危险时先保护客栈和无辜者。", Knowledge: "知道失踪信使曾在今晚来过；知道柜台下的染血信蜡，但不会主动向陌生人承认。", InScene: true},
			{EntityID: "npc:mercenary", DefinitionID: "mercenary.v1", Name: "铁杉", Role: "佣兵", Profile: "寡言、务实、对危险敏感。会根据自己看见和听见的迹象判断，不会凭空知道别人的秘密。", Knowledge: "看见客栈里的人进出和异常动静；不知道沈岚藏着什么。", InScene: true},
		},
		Bystanders: []string{"卖花的老人", "戴蓝围巾的学生", "赶车人", "河运工", "带孩子的旅客", "醉酒的木匠", "灰帽商人", "修钟匠", "披斗篷的妇人", "打瞌睡的船夫"},
	}
}

func characterByID(def gameDefinition, id string) (Character, bool) {
	for _, c := range def.Characters {
		if c.EntityID == strings.TrimSpace(id) {
			return c, true
		}
	}
	return Character{}, false
}

func defaultAddressee(input string) string {
	value := strings.ToLower(input)
	switch {
	case strings.Contains(value, "老板"), strings.Contains(value, "沈岚"), strings.Contains(value, "innkeeper"):
		return "npc:innkeeper"
	case strings.Contains(value, "佣兵"), strings.Contains(value, "铁杉"), strings.Contains(value, "mercenary"):
		return "npc:mercenary"
	default:
		return ""
	}
}

func isPrivateInput(input string) bool {
	value := strings.ToLower(input)
	for _, marker := range []string{"私下", "耳语", "低声", "悄悄", "只对", "whisper", "quietly", "secret"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func cleanText(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
}
