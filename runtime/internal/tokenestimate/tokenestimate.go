package tokenestimate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// EstimateText returns a deterministic provider-neutral token estimate.
func EstimateText(value string) int {
	total := 0
	asciiRunes := 0
	flushASCII := func() {
		if asciiRunes == 0 {
			return
		}
		total += (asciiRunes + 3) / 4
		asciiRunes = 0
	}

	for _, r := range value {
		if isASCIIWordLike(r) {
			asciiRunes++
			continue
		}
		flushASCII()
		total++
	}
	flushASCII()
	return total
}

// CompactStableJSON returns compact JSON with Go's deterministic map-key order.
func CompactStableJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// EstimateStableJSON estimates the compact JSON representation of value.
func EstimateStableJSON(value any) (int, error) {
	data, err := CompactStableJSON(value)
	if err != nil {
		return 0, err
	}
	return EstimateText(data), nil
}

// EstimateJSONDocument estimates a parsed JSON document after stable compaction.
func EstimateJSONDocument(document string) (int, error) {
	value, err := ParseJSONDocument(document)
	if err != nil {
		return 0, err
	}
	return EstimateStableJSON(value)
}

// ParseJSONDocument parses exactly one JSON document and preserves number text.
func ParseJSONDocument(document string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("json document contains multiple values")
		}
		return nil, err
	}
	return value, nil
}

func isASCIIWordLike(r rune) bool {
	return r == ' ' ||
		r == '\n' ||
		r == '\r' ||
		r == '\t' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}
