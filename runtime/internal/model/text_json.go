package model

import (
	"encoding/json"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// ValidateTextJSON checks JSON syntax and Unicode without repairing malformed text.
func ValidateTextJSON(data []byte) error {
	if !utf8.Valid(data) || !json.Valid(data) {
		return ErrInvalidTextResponse
	}

	// Valid JSON guarantees complete string escapes. Consume each escape so an
	// escaped backslash cannot be interpreted as a new Unicode escape.
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		first, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		if !utf16.IsSurrogate(rune(first)) {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return ErrInvalidTextResponse
		}
		second, _ := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if utf16.DecodeRune(rune(first), rune(second)) == utf8.RuneError {
			return ErrInvalidTextResponse
		}
		i += 6
	}
	return nil
}
