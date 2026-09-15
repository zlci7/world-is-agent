package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// HistoryTextSpan uses half-open Unicode rune offsets in the original string.
type HistoryTextSpan struct {
	Path  string
	Start int
	End   int
}

type HistoryTextField struct {
	Path    string
	ActorID string
	Kind    string
	Text    string
}

// HistoryTextFields identifies displayable text by canonical JSON field, without
// attributing model arguments or game results to an inferred speaker.
func HistoryTextFields(batch HistoryBatch) []HistoryTextField {
	data, err := json.Marshal(batch)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil
	}
	var fields []HistoryTextField
	appendFacts := func(value any, path string) {
		for i, value := range historyTextArray(value) {
			fact := historyTextObject(value)
			text, _ := fact["Text"].(string)
			if text == "" {
				continue
			}
			actor, _ := fact["ActorEntityID"].(string)
			kind, _ := fact["Kind"].(string)
			fields = append(fields, HistoryTextField{Path: fmt.Sprintf("%s/%d/Text", path, i), ActorID: actor, Kind: kind, Text: text})
		}
	}
	appendFacts(historyTextObject(root["event"])["facts"], "/event/facts")
	appendFacts(historyTextObject(root["legacy"])["SourceContextFacts"], "/legacy/SourceContextFacts")
	if reason, _ := historyTextObject(root["task_result"])["reason"].(string); reason != "" {
		fields = append(fields, HistoryTextField{Path: "/task_result/reason", Kind: "task_result", Text: reason})
	}
	for i, value := range historyTextArray(root["steps"]) {
		step := historyTextObject(value)
		path := fmt.Sprintf("/steps/%d", i)
		for j, call := range historyTextArray(historyTextObject(step["decision"])["ToolCalls"]) {
			appendHistoryStringLeaves(&fields, historyTextObject(call)["Arguments"], fmt.Sprintf("%s/decision/ToolCalls/%d/Arguments", path, j), "tool_argument")
		}
		for j, value := range historyTextArray(step["executions"]) {
			execution := historyTextObject(value)
			path := fmt.Sprintf("%s/executions/%d", path, j)
			appendHistoryStringLeaves(&fields, historyTextObject(execution["call"])["Arguments"], path+"/call/Arguments", "tool_argument")
			result := historyTextObject(execution["action_result"])
			appendHistoryStringLeaves(&fields, result["output"], path+"/action_result/output", "action_output")
			appendHistoryStringLeaves(&fields, result["error"], path+"/action_result/error", "action_error")
		}
	}
	return fields
}

func appendHistoryStringLeaves(fields *[]HistoryTextField, value any, path, kind string) {
	switch value := value.(type) {
	case string:
		if value != "" {
			*fields = append(*fields, HistoryTextField{Path: path, Kind: kind, Text: value})
		}
	case []any:
		for i, item := range value {
			appendHistoryStringLeaves(fields, item, fmt.Sprintf("%s/%d", path, i), kind)
		}
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			appendHistoryStringLeaves(fields, value[key], path+"/"+escaped, kind)
		}
	}
}

func historyTextObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func historyTextArray(value any) []any {
	items, _ := value.([]any)
	return items
}
