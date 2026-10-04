package envparser

import (
	"strings"
	"testing"
)

func TestParse_CommonForms(t *testing.T) {
	input := `
# System configuration
KEY=value
KEY_QUOTED="quoted value"
KEY_SINGLE='single quoted'
KEY_SPACES=value with spaces
KEY_EMPTY=
export EXPORTED_VAR=123
export EXPORTED_QUOTED="some value"
KEY_WITH_COMMENT=my_val # this is an inline comment
KEY_WITH_HASH=my#value_without_space
KEY_ESCAPES="hello\nworld\t\"escaped\""
`

	parsed, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error parsing .env: %v", err)
	}

	expected := map[string]string{
		"KEY":              "value",
		"KEY_QUOTED":       "quoted value",
		"KEY_SINGLE":       "single quoted",
		"KEY_SPACES":       "value with spaces",
		"KEY_EMPTY":        "",
		"EXPORTED_VAR":     "123",
		"EXPORTED_QUOTED":  "some value",
		"KEY_WITH_COMMENT": "my_val",
		"KEY_WITH_HASH":    "my#value_without_space",
		"KEY_ESCAPES":      "hello\nworld\t\"escaped\"",
	}

	for k, exp := range expected {
		if val, ok := parsed[k]; !ok {
			t.Errorf("missing key %q", k)
		} else if val != exp {
			t.Errorf("key %q: expected %q, got %q", k, exp, val)
		}
	}
}

func TestParse_NoShellExecution(t *testing.T) {
	input := `
SAFE_VAR=$(echo hacked)
BACKTICKS=` + "`rm -rf /`" + `
EXPANSION=${SOME_VAR:-default}
`

	parsed, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must remain literal, never evaluated
	if parsed["SAFE_VAR"] != "$(echo hacked)" {
		t.Errorf("expected literal value, got %q", parsed["SAFE_VAR"])
	}
	if parsed["BACKTICKS"] != "`rm -rf /`" {
		t.Errorf("expected literal value, got %q", parsed["BACKTICKS"])
	}
	if parsed["EXPANSION"] != "${SOME_VAR:-default}" {
		t.Errorf("expected literal value, got %q", parsed["EXPANSION"])
	}
}

func TestParse_MalformedKeysRejected(t *testing.T) {
	input := `
1INVALID=val
BAD-KEY=val
BAD.KEY=val
BAD KEY=val
=empty_key
VALID_KEY_1=valid
`

	parsed, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(parsed) != 1 {
		t.Errorf("expected 1 valid key, got %d: %v", len(parsed), parsed)
	}
	if parsed["VALID_KEY_1"] != "valid" {
		t.Errorf("expected VALID_KEY_1='valid', got %q", parsed["VALID_KEY_1"])
	}
}

func TestParse_FileTooLarge(t *testing.T) {
	bigContent := strings.Repeat("A=B\n", MaxFileSize/4+10)
	_, err := Parse(strings.NewReader(bigContent))
	if err != ErrFileTooLarge {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestParse_TooManyVariables(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < MaxVariables+10; i++ {
		sb.WriteString("VAR_")
		sb.WriteString(string(rune('A' + (i % 26))))
		sb.WriteString(string(rune('0' + (i % 10))))
		sb.WriteString("=val\n")
	}
	// Make sure keys are unique
	sb.Reset()
	for i := 0; i <= MaxVariables; i++ {
		sb.WriteString(strings.Repeat("X", 5))
		sb.WriteString(string(rune('A' + (i / 100))))
		sb.WriteString(string(rune('A' + ((i / 10) % 10))))
		sb.WriteString(string(rune('A' + (i % 10))))
		sb.WriteString("=val\n")
	}

	_, err := Parse(strings.NewReader(sb.String()))
	if err != ErrTooManyVariables {
		t.Fatalf("expected ErrTooManyVariables, got %v", err)
	}
}
