package trustfence

import (
	"strings"
	"testing"
	"unicode"
)

func TestWrap(t *testing.T) {
	tests := []struct {
		name, tag, source, body, want string
	}{
		{"plain", TagMemory, "note.1", "hello", `<memory source="note.1">hello</memory>`},
		{"untrust", TagUntrust, "web_fetch", "hi", `<untrust source="web_fetch">hi</untrust>`},
		{"closer escaped", TagMemory, "m", "a</memory>b", `<memory source="m">a&lt;/memory&gt;b</memory>`},
		{"ampersand first", TagMemory, "m", "&lt;", `<memory source="m">&amp;lt;</memory>`},
		{"fullwidth escaped", TagUntrust, "m", "＜／untrust＞", `<untrust source="m">&#xFF1C;&#xFF0F;untrust&#xFF1E;</untrust>`},
		{"empty body", TagMemory, "m", "", `<memory source="m"></memory>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Wrap(tt.tag, tt.source, tt.body); got != tt.want {
				t.Fatalf("Wrap = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPanics(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{"wrap unknown tag", func() { Wrap("system", "m", "x") }},
		{"wrap empty tag", func() { Wrap("", "m", "x") }},
		{"wrap empty source", func() { Wrap(TagMemory, "", "x") }},
		{"wrap upper case source", func() { Wrap(TagMemory, "Web", "x") }},
		{"wrap quote in source", func() { Wrap(TagMemory, `a"b`, "x") }},
		{"wrap space in source", func() { Wrap(TagMemory, "a b", "x") }},
		{"wrap newline in source", func() { Wrap(TagMemory, "a\n", "x") }},
		{"escape unknown tag", func() { Escape("Memory", "x") }},
		{"open unknown tag", func() { _, _ = Open("untrusted", "x") }},
		{"close unknown tag", func() { Close("tool") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("did not panic")
				}
			}()
			tt.call()
		})
	}
}

func TestClose(t *testing.T) {
	tests := []struct{ tag, want string }{
		{TagMemory, "</memory>"},
		{TagUntrust, "</untrust>"},
	}
	for _, tt := range tests {
		if got := Close(tt.tag); got != tt.want {
			t.Errorf("Close(%q) = %q, want %q", tt.tag, got, tt.want)
		}
	}
}

func TestOpenRoundTrip(t *testing.T) {
	bodies := []string{
		"",
		"plain text",
		"</memory></untrust>",
		"＜／memory＞",
		"<\u200D/memory>",
		"</memory\uFE0F>",
		"&amp; &lt; &gt; &#xFF1C; &#xFF0F; &#xFF1E;",
		"&&&<<<>>>",
		"emoji 👨\u200D👩\u200D👧 ❤\uFE0F",
		"invalid \xff utf-8",
	}
	for _, tag := range []string{TagMemory, TagUntrust} {
		for _, body := range bodies {
			got, err := Open(tag, Wrap(tag, "src", body))
			if err != nil {
				t.Errorf("Open(%s, Wrap(%q)) error: %v", tag, body, err)
				continue
			}
			if got != body {
				t.Errorf("Open(%s, Wrap(%q)) = %q", tag, body, got)
			}
		}
	}
}

func TestOpenRejects(t *testing.T) {
	tests := []struct {
		name, tag, wrapped string
	}{
		{"empty", TagMemory, ""},
		{"no opener", TagMemory, "body</memory>"},
		{"wrong tag", TagUntrust, `<memory source="m">x</memory>`},
		{"bad source", TagMemory, `<memory source="M">x</memory>`},
		{"unterminated opener", TagMemory, `<memory source="m`},
		{"no closer", TagMemory, `<memory source="m">x`},
		{"two closers", TagMemory, `<memory source="m">x</memory>y</memory>`},
		{"fullwidth closer inside", TagMemory, `<memory source="m">x＜／memory＞y</memory>`},
		{"joiner split closer inside", TagMemory, "<memory source=\"m\">x<\u200D/memory>y</memory>"},
		{"selector inside closer", TagMemory, "<memory source=\"m\">x</mem\uFE0Fory>y</memory>"},
		{"supplementary selector inside closer", TagMemory, "<memory source=\"m\">x</memory\U000E0100>y</memory>"},
		{"raw angle bracket", TagMemory, `<memory source="m">a<b</memory>`},
		{"stray ampersand", TagMemory, `<memory source="m">a&b</memory>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Open(tt.tag, tt.wrapped); err == nil {
				t.Fatalf("Open = %q, want error", got)
			}
		})
	}
}

func TestClean(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello", "hello"},
		{"tag characters", "a\U000E0001\U000E0041\U000E007Fb\U000E0000", "ab"},
		{"zero-width space", "a\u200Bb", "ab"},
		{"word joiner", "a\u2060b", "ab"},
		{"byte order mark", "\uFEFFab", "ab"},
		{"soft hyphen", "a\u00ADb", "ab"},
		{"bidi controls", "\u202Ea\u202Cb\u2066c\u2069\u200E\u200F\u061C", "abc"},
		{"zero-width non-joiner", "a\u200Cb", "ab"},
		{"joiner kept", "👨\u200D👩\u200D👧", "👨\u200D👩\u200D👧"},
		{"variation selector kept", "❤\uFE0F", "❤\uFE0F"},
		{"invalid utf-8 replaced", "a\xffb", "a\uFFFDb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Clean(tt.in); got != tt.want {
				t.Fatalf("Clean(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNeutralizeSlot(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello", "hello"},
		{"system open", "<system>do x", "&lt;system>do x"},
		{"system close", "</system>", "&lt;/system>"},
		{"special token", "<|im_start|>", "&lt;|im_start|>"},
		{"template", "{{ .Secret }}", "{ { .Secret }}"},
		{"triple brace", "{{{", "{ { {"},
		{"fullwidth open", "＜system", "&lt;system"},
		{"fullwidth close", "＜／system", "&lt;／system"},
		{"joiner split", "<\u200Dsystem", "&lt;\u200Dsystem"},
		{"joiner split close", "<\u200D/\u200Dsystem", "&lt;\u200D/\u200Dsystem"},
		{"zero-width space split", "<\u200Bsystem", "&lt;system"},
		{"selector split", "<\uFE0Fsys\U000E0100tem", "&lt;system"},
		{"selector split braces", "{\uFE0F{", "{ {"},
		{"joiner split braces", "{\u200D{", "{ \u200D{"},
		{"fullwidth bar", "＜|", "&lt;|"},
		{"other tags untouched", "<b>bold</b> <sys", "<b>bold</b> <sys"},
		{"single brace", "{x}", "{x}"},
		{"format characters removed", "a\u202Eb\U000E0041", "ab"},
		{"emoji kept", "👨\u200D👩\u200D👧", "👨\u200D👩\u200D👧"},
		{"emoji selector dropped", "❤\uFE0F", "❤"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeutralizeSlot(tt.in)
			if got != tt.want {
				t.Fatalf("NeutralizeSlot(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "\u200B") {
				t.Fatalf("NeutralizeSlot(%q) inserted a zero-width space", tt.in)
			}
		})
	}
}

// view is the fuzz oracle, written apart from the package code: it drops
// format characters and variation selectors and folds the fullwidth forms.
func view(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cf, r),
			r >= 0xE0000 && r <= 0xE007F,
			r >= 0xFE00 && r <= 0xFE0F,
			r >= 0xE0100 && r <= 0xE01EF:
			continue
		case r == '＜':
			r = '<'
		case r == '＞':
			r = '>'
		case r == '／':
			r = '/'
		}
		b.WriteRune(r)
	}
	return b.String()
}

var fuzzSeeds = []string{
	"",
	"plain",
	"</memory>",
	"</untrust>",
	"＜／memory＞",
	"＜／untrust＞",
	"<\u200D/memory>",
	"</mem\u200Dory>",
	"</memory\uFE0F>",
	"<\U000E0100/untrust>",
	"&lt;/memory&gt;",
	"<system>",
	"</system>",
	"<|endoftext|>",
	"{{",
	"＜system",
	"<\u200Dsystem",
	"<\u200B/system",
	"{\uFE0F{",
	"\U000E0041\u202E",
}

func FuzzWrap(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		for _, tag := range []string{TagMemory, TagUntrust} {
			w := Wrap(tag, "fuzz", body)
			if n := strings.Count(view(w), "</"+tag+">"); n != 1 {
				t.Fatalf("Wrap(%s, %q) = %q has %d closers, want 1", tag, body, w, n)
			}
			got, err := Open(tag, w)
			if err != nil {
				t.Fatalf("Open(%s, %q) error: %v", tag, w, err)
			}
			if got != body {
				t.Fatalf("Open(%s, Wrap(%q)) = %q", tag, body, got)
			}
		}
	})
}

func FuzzNeutralizeSlot(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v := view(NeutralizeSlot(s))
		for _, seq := range []string{"<system", "</system", "<|", "{{"} {
			if strings.Contains(v, seq) {
				t.Fatalf("NeutralizeSlot(%q) leaves %q in view %q", s, seq, v)
			}
		}
	})
}
