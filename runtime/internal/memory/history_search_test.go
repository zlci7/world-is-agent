package memory

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestHistoryQueryDefaultBoundaries(t *testing.T) {
	for _, size := range []int{255, 256, 257} {
		text := strings.Repeat("!", size-2) + "\u9493\u9c7c"
		terms := HistoryQueryTerms(text, 256, 32)
		if (len(terms) == 1) != (size <= 256) {
			t.Fatalf("unicode input chars=%d terms=%v", size, terms)
		}
	}
	for _, size := range []int{31, 32, 33} {
		values := make([]string, size)
		for i := range values {
			values[i] = fmt.Sprintf("w%d", i)
		}
		terms := HistoryQueryTerms(strings.Join(values, " "), 256, 32)
		if len(terms) != min(size, 32) {
			t.Fatalf("query terms=%d got=%d", size, len(terms))
		}
	}
}

func TestHistoryLiteralQuery(t *testing.T) {
	for _, tt := range []struct {
		input        string
		chars, terms int
		want         []string
	}{
		{"\u9493\u9c7c\u65f6\uff0c7319 LinUS linus", 256, 32, []string{"\u9493\u9c7c", "\u9c7c\u65f6", "7319", "linus"}},
		{"\u9493 ! ?", 256, 32, nil},
		{"\u9493\u9c7c\u9493\u9c7c", 256, 32, []string{"\u9493\u9c7c", "\u9c7c\u9493"}},
		{"\u9493\u9c7c\u65f6", 2, 32, []string{"\u9493\u9c7c"}},
		{"one two three", 256, 2, []string{"one", "two"}},
	} {
		got := HistoryQueryTerms(tt.input, tt.chars, tt.terms)
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%q: got %v want %v", tt.input, got, tt.want)
		}
	}
}

func TestHistoryIndexLimits(t *testing.T) {
	limits := DefaultHistoryIndexLimits()
	if err := limits.Validate(); err != nil {
		t.Fatal(err)
	}
	batch := HistoryBatch{Event: HistoryEvent{Facts: []SourceContextFact{{Text: strings.Repeat("a", 10)}}}}
	limits.TextBytes = 10
	fields, status := historyIndexFields(batch, limits)
	if status != "ready" || len(fields) != 1 {
		t.Fatalf("equal boundary: %s %v", status, fields)
	}
	limits.TextBytes = 9
	fields, status = historyIndexFields(batch, limits)
	if status != "capacity_exceeded" || len(fields) != 0 {
		t.Fatalf("over boundary: %s %v", status, fields)
	}
}

func TestHistoryIndexFragmentAndTermBoundaries(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		limits := DefaultHistoryIndexLimits()
		limits.Fragments = 2
		batch := HistoryBatch{}
		for i := 0; i < n; i++ {
			batch.Event.Facts = append(batch.Event.Facts, SourceContextFact{Text: "one"})
		}
		_, status := historyIndexFields(batch, limits)
		if (status == "ready") != (n <= 2) {
			t.Fatalf("fragments %d: %s", n, status)
		}
		limits = DefaultHistoryIndexLimits()
		limits.TermAssociations = 2
		batch.Event.Facts = []SourceContextFact{{Text: strings.Join([]string{"one", "two", "three"}[:n], " ")}}
		_, status = historyIndexFields(batch, limits)
		if (status == "ready") != (n <= 2) {
			t.Fatalf("terms %d: %s", n, status)
		}
	}
	limits := DefaultHistoryIndexLimits()
	limits.TermAssociations = 1
	if _, status := historyIndexFields(HistoryBatch{Event: HistoryEvent{Facts: []SourceContextFact{{Text: "same same same"}}}}, limits); status != "ready" {
		t.Fatal("associations are unique per fragment")
	}
	for _, invalid := range []HistoryIndexLimits{{-1, 1, 1}, {1, -1, 1}, {1, 1, -1}} {
		if invalid.Validate() == nil {
			t.Fatal("invalid capacity accepted")
		}
	}
}
