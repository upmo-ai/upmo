package domain

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

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

func Prepare(text string) (string, bool) {
	text = strings.TrimSpace(stripFencedCodeBlocks(text))
	runes := []rune(text)
	if len(runes) > 4000 {
		cut := 4000
		for i := 3999; i >= 3920; i-- {
			if runes[i] == '\n' || runes[i] == ' ' {
				cut = i
				break
			}
		}
		text = strings.TrimSpace(string(runes[:cut]))
	}
	if utf8.RuneCountInString(text) < 12 {
		return "", false
	}
	return text, true
}

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

func stripFencedCodeBlocks(s string) string {
	for {
		open := strings.Index(s, "```")
		if open < 0 {
			return s
		}
		remaining := s[open+3:]
		closeOffset := strings.Index(remaining, "```")
		if closeOffset < 0 {
			return s[:open]
		}
		after := remaining[closeOffset+3:]
		separator := ""
		if open > 0 && len(after) > 0 {
			beforeRune, _ := utf8.DecodeLastRuneInString(s[:open])
			afterRune, _ := utf8.DecodeRuneInString(after)
			if !unicode.IsSpace(beforeRune) && !unicode.IsSpace(afterRune) {
				separator = " "
			}
		}
		s = s[:open] + separator + after
	}
}
