package tokenestimate

import "testing"

func TestEstimateTextCountsCJKRunesConservatively(t *testing.T) {
	text := repeatRune('\u6625', 100)

	if got, want := EstimateText(text), 100; got != want {
		t.Fatalf("EstimateText(CJK) = %d, want %d", got, want)
	}
}

func TestEstimateTextCompactsASCIIWordRuns(t *testing.T) {
	text := repeatRune('a', 100)

	if got, want := EstimateText(text), 25; got != want {
		t.Fatalf("EstimateText(ASCII run) = %d, want %d", got, want)
	}
}

func TestEstimateTextHandlesMixedRunsAndPunctuation(t *testing.T) {
	text := "hello, \u6625\u5929!"

	if got, want := EstimateText(text), 7; got != want {
		t.Fatalf("EstimateText(mixed) = %d, want %d", got, want)
	}
}

func TestEstimateTextCountsJSONPunctuationIndividually(t *testing.T) {
	text := `{"x":1,"y":2}`

	if got, want := EstimateText(text), 13; got != want {
		t.Fatalf("EstimateText(JSON punctuation) = %d, want %d", got, want)
	}
}

func TestEstimateTextCountsEmojiAndFullWidthPunctuationConservatively(t *testing.T) {
	text := "\U0001f642\uff0c\u3002"

	if got, want := EstimateText(text), 3; got != want {
		t.Fatalf("EstimateText(emoji/full-width punctuation) = %d, want %d", got, want)
	}
}

func TestEstimateStableJSONIsDeterministicForMapOrder(t *testing.T) {
	left := map[string]any{"b": 2, "a": "\u6625"}
	right := map[string]any{"a": "\u6625", "b": 2}

	leftJSON, err := CompactStableJSON(left)
	if err != nil {
		t.Fatalf("CompactStableJSON(left) error = %v", err)
	}
	rightJSON, err := CompactStableJSON(right)
	if err != nil {
		t.Fatalf("CompactStableJSON(right) error = %v", err)
	}
	if leftJSON != rightJSON {
		t.Fatalf("CompactStableJSON differs by map insertion order:\nleft=%s\nright=%s", leftJSON, rightJSON)
	}

	leftTokens, err := EstimateStableJSON(left)
	if err != nil {
		t.Fatalf("EstimateStableJSON(left) error = %v", err)
	}
	rightTokens, err := EstimateStableJSON(right)
	if err != nil {
		t.Fatalf("EstimateStableJSON(right) error = %v", err)
	}
	if leftTokens != rightTokens {
		t.Fatalf("EstimateStableJSON differs by map insertion order: left=%d right=%d", leftTokens, rightTokens)
	}
}

func TestEstimateStableJSONIncreasesWithAdditionalContent(t *testing.T) {
	base, err := EstimateStableJSON(map[string]any{"text": "\u6625"})
	if err != nil {
		t.Fatalf("EstimateStableJSON(base) error = %v", err)
	}
	larger, err := EstimateStableJSON(map[string]any{"text": "\u6625\u5929"})
	if err != nil {
		t.Fatalf("EstimateStableJSON(larger) error = %v", err)
	}
	if larger <= base {
		t.Fatalf("larger estimate = %d, want greater than base %d", larger, base)
	}
}

func TestEstimateJSONDocumentNormalizesEquivalentJSON(t *testing.T) {
	compact := `{"description":"你好","type":"object"}`
	escaped := `{"type":"object","description":"\u4f60\u597d"}`
	pretty := `{
  "type": "object",
  "description": "你好"
}`

	compactTokens, err := EstimateJSONDocument(compact)
	if err != nil {
		t.Fatalf("EstimateJSONDocument(compact) error = %v", err)
	}
	for _, item := range []struct {
		name string
		json string
	}{
		{name: "escaped", json: escaped},
		{name: "pretty", json: pretty},
	} {
		got, err := EstimateJSONDocument(item.json)
		if err != nil {
			t.Fatalf("EstimateJSONDocument(%s) error = %v", item.name, err)
		}
		if got != compactTokens {
			t.Fatalf("EstimateJSONDocument(%s) = %d, want %d", item.name, got, compactTokens)
		}
	}
}

func TestParseJSONDocumentRequiresSingleCompleteDocument(t *testing.T) {
	if _, err := ParseJSONDocument(`{"type":"object"}   `); err != nil {
		t.Fatalf("ParseJSONDocument(valid with trailing whitespace) error = %v", err)
	}
	for _, input := range []string{
		`{"type":"object"} trailing`,
		`{"type":"object"} {}`,
	} {
		if _, err := ParseJSONDocument(input); err == nil {
			t.Fatalf("ParseJSONDocument(%q) succeeded, want error", input)
		}
	}
}

func repeatRune(r rune, count int) string {
	out := make([]rune, count)
	for i := range out {
		out[i] = r
	}
	return string(out)
}
