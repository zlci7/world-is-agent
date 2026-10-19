package tool

import (
	"strings"
	"testing"
)

func TestValidateArgumentsAppliesCommonInputSchemaConstraints(t *testing.T) {
	schema := `{
		"type":"object",
		"properties":{
			"mood":{"type":"string","enum":["happy","sad"]},
			"intensity":{"type":"integer"}
		},
		"required":["mood"],
		"additionalProperties":false
	}`

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "valid",
			args: map[string]any{"mood": "happy", "intensity": 2},
		},
		{
			name: "missing required",
			args: map[string]any{"intensity": 2},
			want: `missing required argument "mood"`,
		},
		{
			name: "enum mismatch",
			args: map[string]any{"mood": "angry"},
			want: `argument "mood" must match enum`,
		},
		{
			name: "additional property",
			args: map[string]any{"mood": "happy", "extra": true},
			want: `unexpected argument "extra"`,
		},
		{
			name: "type mismatch",
			args: map[string]any{"mood": "sad", "intensity": "high"},
			want: `argument "intensity" must be integer`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArguments(schema, tt.args)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ValidateArguments returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateArguments error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateArgumentsStringLengthsUseUnicodeCharacters(t *testing.T) {
	schema := `{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":2}},"required":["query"]}`
	for _, tc := range []struct {
		value string
		valid bool
	}{{"", false}, {"鱼", true}, {"鱼🐟", true}, {"鱼🐟鱼", false}, {"ab", true}, {"abc", false}} {
		err := ValidateArguments(schema, map[string]any{"query": tc.value})
		if (err == nil) != tc.valid {
			t.Fatalf("query=%q valid=%v error=%v", tc.value, tc.valid, err)
		}
	}
}
