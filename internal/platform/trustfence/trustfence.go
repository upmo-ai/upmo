// Package trustfence wraps untrusted text in trust fences and neutralises
// control sequences before the text enters a prompt.
//
// Closer and control-sequence matching folds the fullwidth forms ＜ ＞ ／ to
// ASCII and ignores the zero-width joiner and variation selectors, so a
// lookalike or a split token is still recognised.
package trustfence

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The only fence tags.
const (
	TagMemory  = "memory"
	TagUntrust = "untrust"
)

const zeroWidthJoiner = '\u200D'

var sourcePattern = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// escapes is applied in order by Escape and in reverse by unescape. The
// ampersand comes first so the result is reversible.
var escapes = [][2]string{
	{"&", "&amp;"},
	{"<", "&lt;"},
	{">", "&gt;"},
	{"\uFF1C", "&#xFF1C;"},
	{"\uFF1E", "&#xFF1E;"},
	{"\uFF0F", "&#xFF0F;"},
}

// Wrap returns body escaped and fenced in tag, labelled with source. It
// panics on an unknown tag or a source that does not match ^[a-z0-9_.-]+$.
func Wrap(tag, source, body string) string {
	mustTag(tag)
	if !sourcePattern.MatchString(source) {
		panic("trustfence: invalid source " + strconv.Quote(source))
	}
	return opener(tag, source) + Escape(tag, body) + Close(tag)
}

// Open checks the fence envelope of wrapped, requires exactly one closer and
// returns the original body. It panics on an unknown tag.
func Open(tag, wrapped string) (string, error) {
	mustTag(tag)
	rest, ok := strings.CutPrefix(wrapped, "<"+tag+` source="`)
	if !ok {
		return "", errors.New("trustfence: missing opener")
	}
	source, rest, ok := strings.Cut(rest, `">`)
	if !ok || !sourcePattern.MatchString(source) {
		return "", errors.New("trustfence: invalid source")
	}
	body, ok := strings.CutSuffix(rest, Close(tag))
	if !ok {
		return "", errors.New("trustfence: missing closer")
	}
	if n := strings.Count(fold(wrapped), Close(tag)); n != 1 {
		return "", errors.New("trustfence: want exactly one closer")
	}
	orig := unescape(body)
	if Escape(tag, orig) != body {
		return "", errors.New("trustfence: body is not escaped")
	}
	return orig, nil
}

// Close returns the closer for tag. It panics on an unknown tag.
func Close(tag string) string {
	mustTag(tag)
	return "</" + tag + ">"
}

// Escape makes body unable to contain the closer for tag, in any folded form.
// It is reversible. It panics on an unknown tag.
func Escape(tag, body string) string {
	mustTag(tag)
	for _, e := range escapes {
		body = strings.ReplaceAll(body, e[0], e[1])
	}
	return body
}

// Clean removes every Unicode format character (category Cf) and every tag
// character (U+E0000 to U+E007F), except the zero-width joiner, which emoji
// need. Invalid UTF-8 becomes U+FFFD, so removing a character never joins
// the bytes around it into a new one.
func Clean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r != zeroWidthJoiner && (unicode.Is(unicode.Cf, r) || (r >= 0xE0000 && r <= 0xE007F)) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NeutralizeSlot cleans s, drops variation selectors and turns the control
// sequences <system, </system, <| and {{ into the visible text &lt;system,
// &lt;/system, &lt;| and { {. Matching folds fullwidth forms and skips the
// zero-width joiner, so a lookalike or joiner-split sequence is neutralised
// too.
func NeutralizeSlot(s string) string {
	var rs []rune
	for _, r := range Clean(s) {
		if !isSelector(r) {
			rs = append(rs, r)
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range rs {
		next := rs[i+1:]
		switch {
		case foldRune(r) == '<' && (startsWith(next, "system") || startsWith(next, "/system") || startsWith(next, "|")):
			b.WriteString("&lt;")
		case foldRune(r) == '{' && startsWith(next, "{"):
			b.WriteString("{ ")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func opener(tag, source string) string {
	return "<" + tag + ` source="` + source + `">`
}

func unescape(s string) string {
	for i := len(escapes) - 1; i >= 0; i-- {
		s = strings.ReplaceAll(s, escapes[i][1], escapes[i][0])
	}
	return s
}

func mustTag(tag string) {
	if tag != TagMemory && tag != TagUntrust {
		panic("trustfence: unknown tag " + strconv.Quote(tag))
	}
}

// fold returns s with fullwidth forms folded to ASCII and joiners and
// variation selectors removed, the view used to count closers.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !ignorable(r) {
			b.WriteRune(foldRune(r))
		}
	}
	return b.String()
}

// startsWith reports whether rs begins with tok once joiners and variation
// selectors are skipped and fullwidth forms are folded.
func startsWith(rs []rune, tok string) bool {
	i := 0
	for _, want := range tok {
		for i < len(rs) && ignorable(rs[i]) {
			i++
		}
		if i == len(rs) || foldRune(rs[i]) != want {
			return false
		}
		i++
	}
	return true
}

func foldRune(r rune) rune {
	switch r {
	case '\uFF1C':
		return '<'
	case '\uFF1E':
		return '>'
	case '\uFF0F':
		return '/'
	case '\uFF5B':
		return '{'
	case '\uFF5D':
		return '}'
	}
	return r
}

func ignorable(r rune) bool {
	return r == zeroWidthJoiner || isSelector(r)
}

func isSelector(r rune) bool {
	return (r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF)
}
