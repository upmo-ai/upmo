package domain

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// LessonTitleRunes bounds the title, which is half of an index line.
	LessonTitleRunes = 80
	// LessonCueRunes bounds the cue, the one line that says when to open it.
	LessonCueRunes = 160
	// LessonBodyRunes bounds the body, which is loaded only when opened.
	LessonBodyRunes = 4000
	// LessonIndexCap keeps the non-inline index small enough to show every time.
	LessonIndexCap = 24
)

var (
	// ErrLesson is a title, cue or body over its rune limit.
	ErrLesson = errors.New("agents: lesson too long")
	// ErrLessonTitle is an empty title.
	ErrLessonTitle = errors.New("agents: lesson needs a title")
	// ErrLessonCue is an empty cue.
	ErrLessonCue = errors.New("agents: lesson needs a cue")
	// ErrLessonBody is an empty body.
	ErrLessonBody = errors.New("agents: lesson needs a body")
	// ErrLessonDuplicate is a lesson the agent's playbook already holds.
	ErrLessonDuplicate = errors.New("agents: lesson already stored")
	// ErrLessonGone is a lesson that is missing or not visible to the caller.
	ErrLessonGone = errors.New("agents: lesson not found")
	// ErrLessonFull is an agent's non-inline index already at LessonIndexCap.
	ErrLessonFull = errors.New("agents: playbook is full")
)

// Card is one index line for non-inline house rules. Never carries Body.
type Card struct {
	ID    string
	Title string
	Cue   string
}

// Lesson is one full house rule (always_inline or opened on demand).
type Lesson struct {
	ID           string
	AgentID      string
	Title        string
	Cue          string
	Body         string
	AlwaysInline bool
}

// CleanLesson validates, trims, and redacts lesson fields.
func CleanLesson(title, cue, body string) (string, string, string, error) {
	title = strings.TrimSpace(title)
	cue = strings.TrimSpace(cue)
	body = strings.TrimSpace(body)
	switch {
	case title == "":
		return "", "", "", ErrLessonTitle
	case cue == "":
		return "", "", "", ErrLessonCue
	case body == "":
		return "", "", "", ErrLessonBody
	}
	if utf8.RuneCountInString(title) > LessonTitleRunes ||
		utf8.RuneCountInString(cue) > LessonCueRunes ||
		utf8.RuneCountInString(body) > LessonBodyRunes {
		return "", "", "", ErrLesson
	}
	return Redact(title), Redact(cue), Redact(body), nil
}

// AppendInlineLessons composes instructions and any always_inline house rules
// into a prompt overlay. Returns empty if neither instructions nor inline
// lessons exist.
func AppendInlineLessons(instructions string, inline []Lesson) string {
	instructions = strings.TrimSpace(instructions)
	var parts []string
	if instructions != "" {
		parts = append(parts, instructions)
	}
	for _, l := range inline {
		if !l.AlwaysInline || strings.TrimSpace(l.Body) == "" {
			continue
		}
		parts = append(parts, "## "+l.Title+"\n\n"+strings.TrimSpace(l.Body))
	}
	return strings.Join(parts, "\n\n")
}

// AgentSystem builds the cached system prefix text for an agent: its header,
// instructions, and any always_inline house rules. Returns empty if neither
// instructions nor inline lessons exist.
func AgentSystem(name, instructions string, inline []Lesson) string {
	overlay := AppendInlineLessons(instructions, inline)
	if overlay == "" {
		return ""
	}
	return "# Agent: " + name + "\n\n" + overlay
}

var (
	privateKeyPattern  = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	urlUserinfoPattern = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.-]*)://[^\s/@:]+:[^\s/@]+@`)
	jwtPattern         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	awsKeyPattern      = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	googleKeyPattern   = regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{35}\b`)
	bearerPattern      = regexp.MustCompile(`(?i)\b(bearer|token)([ \t]+)([A-Za-z0-9._~+/=-]{12,})`)
	keyedSecretPattern = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api_key|apikey|access_key|secret_key|client_secret|refresh_token|access_token|session_token|token|authorization)\b([ \t]*[:=][ \t]*)([^\s,;]+)`)
	emailPattern       = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

// Redact strips credential-like patterns and email addresses from lesson text.
func Redact(s string) string {
	s = privateKeyPattern.ReplaceAllString(s, "***")
	s = urlUserinfoPattern.ReplaceAllString(s, "${1}://***@")
	s = emailPattern.ReplaceAllString(s, "***")
	s = jwtPattern.ReplaceAllString(s, "***")
	s = awsKeyPattern.ReplaceAllString(s, "***")
	s = googleKeyPattern.ReplaceAllString(s, "***")
	s = redactConditionalValue(s, bearerPattern, 3, 12)
	return redactConditionalValue(s, keyedSecretPattern, 3, 6)
}

func redactConditionalValue(s string, pattern *regexp.Regexp, valueGroup, minimumRunes int) string {
	return pattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := pattern.FindStringSubmatch(match)
		value := parts[valueGroup]
		if utf8.RuneCountInString(value) < minimumRunes || !looksSecret(value) {
			return match
		}
		prefixLength := len(match) - len(value)
		return match[:prefixLength] + "***"
	})
}

func looksSecret(value string) bool {
	for _, r := range value {
		if unicode.IsDigit(r) || unicode.IsUpper(r) || !unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
