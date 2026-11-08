package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestLineRejectsAnEmptyBody(t *testing.T) {
	got, err := Line("  hello  ")
	if err != nil || got != "hello" {
		t.Fatalf("line = %q %v", got, err)
	}
	if _, err := Line("  "); !errors.Is(err, ErrLine) {
		t.Fatalf("blank line error = %v", err)
	}
}

func TestTitleNamesAChatAfterItsFirstUserLine(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no line", "", NewChatTitle},
		{"blank line", " \n\t ", NewChatTitle},
		{"trimmed", "  Plan   the\n\tweek  ", "Plan the week"},
		{"kept at the limit", strings.Repeat("a", TitleRunes), strings.Repeat("a", TitleRunes)},
		{"cut one rune over", strings.Repeat("a", TitleRunes+1), strings.Repeat("a", TitleRunes)},
		{"cut keeps whole runes", strings.Repeat("世", TitleRunes+1), strings.Repeat("世", TitleRunes)},
		{"cut drops a trailing space", strings.Repeat("a ", TitleRunes) + "a", strings.Repeat("a ", 29) + "a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Title(test.body); got != test.want {
				t.Fatalf("title = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPreviewCarriesTheNewestLine(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no line", "", ""},
		{"trimmed", "  The   answer\n", "The answer"},
		{"kept at the limit", strings.Repeat("c", PreviewRunes), strings.Repeat("c", PreviewRunes)},
		{"cut five runes over", strings.Repeat("c", PreviewRunes+5), strings.Repeat("c", PreviewRunes)},
		{"cut drops a trailing space", strings.Repeat("a ", PreviewRunes) + "a", strings.Repeat("a ", 59) + "a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Preview(test.body); got != test.want {
				t.Fatalf("preview = %q, want %q", got, test.want)
			}
		})
	}
}
