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

func TestCustomTitleAndSearchQueryValidation(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":        "",
		"spaces":       " \t\n ",
		"61 a runes":   strings.Repeat("a", TitleRunes+1),
		"61 CJK runes": strings.Repeat("世", TitleRunes+1),
	} {
		if _, err := CustomTitle(raw); !errors.Is(err, ErrTitle) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if got, err := CustomTitle("  Q4 \t Planning  "); err != nil || got != "Q4 Planning" {
		t.Fatalf("title = %q, %v", got, err)
	}
	if got, err := CustomTitle(strings.Repeat("世", TitleRunes)); err != nil || got != strings.Repeat("世", TitleRunes) {
		t.Fatalf("title = %q, %v", got, err)
	}
	if got := DisplayTitle(" Q4 Planning ", "First user line"); got != "Q4 Planning" {
		t.Fatalf("display title = %q", got)
	}
	if got := DisplayTitle("", "  First user line  "); got != "First user line" {
		t.Fatalf("derived display title = %q", got)
	}

	if _, err := SearchQuery("   lead  times "); err != nil {
		t.Fatalf("query error = %v", err)
	}
	if got, err := SearchQuery(""); err != nil || got != "" {
		t.Fatalf("empty query = %q, %v", got, err)
	}
	if got, err := SearchQuery(strings.Repeat("q", QueryRunes)); err != nil || len(got) != QueryRunes {
		t.Fatalf("max query = %d, %v", len(got), err)
	}
	if _, err := SearchQuery(strings.Repeat("q", QueryRunes+1)); !errors.Is(err, ErrQuery) {
		t.Fatalf("long query error = %v", err)
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
