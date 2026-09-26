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
	// 保留这个函数作为旧存档读取时的兼容入口，但只识别明确的称呼短语。
	// 叙事正文中的人物名或职业名不再单独决定玩家正在对谁说话。
	value := strings.ToLower(strings.TrimSpace(input))
	for _, phrase := range []string{"对老板", "向老板", "跟老板", "给老板", "问老板", "告诉老板", "对沈岚", "向沈岚", "跟沈岚", "给沈岚", "问沈岚", "对innkeeper", "向innkeeper", "对佣兵", "向佣兵", "跟佣兵", "给佣兵", "问佣兵", "对铁杉", "向铁杉", "跟铁杉", "给铁杉", "问铁杉", "对mercenary", "向mercenary", "只有老板", "仅老板", "只让老板", "耳语给老板", "低声对老板", "只有沈岚", "仅沈岚", "只让沈岚", "耳语给沈岚", "低声对沈岚", "只有佣兵", "仅佣兵", "只让佣兵", "耳语给佣兵", "低声对佣兵", "只有铁杉", "仅铁杉", "只让铁杉", "耳语给铁杉", "低声对铁杉", "only innkeeper", "only mercenary"} {
		if strings.Contains(value, phrase) {
			if strings.Contains(phrase, "老板") || strings.Contains(phrase, "沈岚") || strings.Contains(phrase, "innkeeper") {
				return "npc:innkeeper"
			}
			return "npc:mercenary"
		}
	}
	return ""
}

func isPrivateInput(input string) bool {
	value := strings.ToLower(input)
	for _, marker := range []string{"私下", "耳语", "低声", "悄悄", "只对", "只有", "仅让", "仅对", "whisper", "quietly", "secret", "only"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func sceneCharacters(characters []Character) []Character {
	result := make([]Character, 0, len(characters))
	for _, character := range characters {
		if character.InScene {
			result = append(result, character)
		}
	}
	return result
}

func findSceneCharacter(characters []Character, id string) (Character, bool) {
	for _, character := range characters {
		if character.InScene && character.EntityID == strings.TrimSpace(id) {
			return character, true
		}
	}
	return Character{}, false
}

func privateInputHint(input, recipient string, characters []Character) bool {
	value := strings.ToLower(strings.TrimSpace(input))
	if isPrivateInput(value) {
		return true
	}
	if recipient == "" {
		return false
	}
	character, ok := findSceneCharacter(characters, recipient)
	if !ok {
		return false
	}
	name := strings.ToLower(character.Name)
	role := strings.ToLower(character.Role)
	aliases := []string{name, role}
	if character.EntityID == "npc:innkeeper" {
		aliases = append(aliases, "innkeeper")
	}
	if character.EntityID == "npc:mercenary" {
		aliases = append(aliases, "mercenary")
	}
	for _, alias := range aliases {
		if alias == "" {
			continue
		}
		if (strings.Contains(value, "只有"+alias) || strings.Contains(value, "仅"+alias) || strings.Contains(value, "only "+alias)) &&
			(strings.Contains(value, "听见") || strings.Contains(value, "听到") || strings.Contains(value, "能听") || strings.Contains(value, "hear")) {
			return true
		}
	}
	return false
}

func cleanText(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
}
