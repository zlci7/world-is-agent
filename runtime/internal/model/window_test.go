package model

import "testing"

func TestWindowLimitsBoundaries(t *testing.T) {
	w := WindowLimits{ContextTokens: 65536, OutputTokens: 8192}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	if w.InputTokens() != 57344 {
		t.Fatalf("input=%d", w.InputTokens())
	}
	for _, n := range []int{57343, 57344} {
		if err := w.Check(n, 8192); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Check(57345, 8192); err == nil {
		t.Fatal("oversized request accepted")
	}
	if err := w.Check(1, 8193); err == nil {
		t.Fatal("oversized output reserve accepted")
	}
	if err := (WindowLimits{}).Check(1, 1); err == nil {
		t.Fatal("undeclared window accepted")
	}
	for _, invalid := range []WindowLimits{{-1, 1}, {1, -1}, {1, 1}, {1, 2}, {65536, 0}, {0, 8192}} {
		if invalid.Validate() == nil {
			t.Fatalf("invalid limits accepted: %+v", invalid)
		}
	}
}
