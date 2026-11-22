package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// retiredWords lists the retired glossary words. The list's source is
// content/glossary.md in the design-system repository.
var retiredWords = []string{
	"mission",
	"missions",
	"sub-mission",
	"harness",
	"harnesses",
	"analytics",
	"inbox",
	"conversation",
	"conversations",
	"destination",
	"destinations",
}

var britishSpellings = map[string]string{
	"unauthorised":  "unauthorized",
	"authorised":    "authorized",
	"authorise":     "authorize",
	"cancelled":     "canceled",
	"cancelling":    "canceling",
	"labelled":      "labeled",
	"labelling":     "labeling",
	"catalogue":     "catalog",
	"catalogues":    "catalogs",
	"catalogued":    "cataloged",
	"colour":        "color",
	"colours":       "colors",
	"summarise":     "summarize",
	"summarised":    "summarized",
	"summariser":    "summarizer",
	"normalise":     "normalize",
	"normalised":    "normalized",
	"normalisation": "normalization",
	"organisation":  "organization",
	"organisations": "organizations",
	"organise":      "organize",
	"organised":     "organized",
	"serialise":     "serialize",
	"serialised":    "serialized",
	"recognise":     "recognize",
	"recognised":    "recognized",
	"sanitise":      "sanitize",
	"prioritise":    "prioritize",
	"neighbour":     "neighbor",
	"neighbours":    "neighbors",
	"centred":       "centered",
	"behaviour":     "behavior",
	"behaviours":    "behaviors",
	"honours":       "honors",
	"defence":       "defense",
	"judgement":     "judgment",
	"licence":       "license",
	"licences":      "licenses",
	"analyse":       "analyze",
	"fulfil":        "fulfill",
}

// termToken is one word in a line, with byte offsets into that line.
type termToken struct {
	text       string
	start, end int
}

// letterTokens splits line into words. Anything that is not a letter is a
// boundary, including hyphens and underscores. A run of letters also splits
// at a lower-to-upper boundary and at an acronym boundary, so MissionID
// yields Mission and HTTPInbox yields Inbox.
func letterTokens(line string) []termToken {
	var out []termToken
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRuneInString(line[i:])
		if !unicode.IsLetter(r) {
			i += size
			continue
		}
		start := i
		i += size
		for i < len(line) {
			r, size = utf8.DecodeRuneInString(line[i:])
			if !unicode.IsLetter(r) {
				break
			}
			i += size
		}
		out = append(out, camelTokens(line, start, i)...)
	}
	return out
}

// camelTokens splits one run of letters on camelCase boundaries.
func camelTokens(line string, start, end int) []termToken {
	type ru struct {
		r   rune
		off int
	}
	var rs []ru
	for off := start; off < end; {
		r, size := utf8.DecodeRuneInString(line[off:])
		rs = append(rs, ru{r, off})
		off += size
	}
	var out []termToken
	part := start
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1].r, rs[i].r
		boundary := unicode.IsLower(prev) && unicode.IsUpper(cur)
		if !boundary && i+1 < len(rs) && unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(rs[i+1].r) {
			boundary = true
		}
		if boundary {
			out = append(out, termToken{line[part:rs[i].off], part, rs[i].off})
			part = rs[i].off
		}
	}
	out = append(out, termToken{line[part:end], part, end})
	return out
}

// retiredInLine returns each retired word in line, in order, as it appears.
// A hyphenated list entry such as sub-mission matches only when its parts
// stand next to each other with a hyphen between them. A longer match wins
// over a shorter one that starts at the same word.
func retiredInLine(line string) []string {
	toks := letterTokens(line)
	var hits []string
	for i := 0; i < len(toks); {
		next, text, ok := longestRetired(line, toks, i)
		if !ok {
			i++
			continue
		}
		hits = append(hits, text)
		i = next
	}
	return hits
}

func britishInLine(line string) []string {
	var hits []string
	for _, token := range letterTokens(line) {
		if _, ok := britishSpellings[strings.ToLower(token.text)]; ok {
			hits = append(hits, token.text)
		}
	}
	return hits
}

func longestRetired(line string, toks []termToken, i int) (next int, text string, ok bool) {
	bestLen := 0
	bestNext := 0
	bestText := ""
	for _, word := range retiredWords {
		parts := strings.Split(word, "-")
		n := len(parts)
		if i+n > len(toks) || !retiredPartsMatch(line, toks[i:i+n], parts) {
			continue
		}
		span := line[toks[i].start:toks[i+n-1].end]
		if len(span) > bestLen {
			bestLen = len(span)
			bestNext = i + n
			bestText = span
		}
	}
	if bestLen == 0 {
		return 0, "", false
	}
	return bestNext, bestText, true
}

func retiredPartsMatch(line string, toks []termToken, parts []string) bool {
	for j, part := range parts {
		if !strings.EqualFold(toks[j].text, part) {
			return false
		}
		if j > 0 && line[toks[j-1].end:toks[j].start] != "-" {
			return false
		}
	}
	return true
}

func normalTermsPath(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(path), "./")
}

// britishSkipped reports paths the British spelling check must not read.
// GitHub's own status value is cancelled, and the password list holds real
// passwords such as cancelled.
func britishSkipped(path string) bool {
	switch normalTermsPath(path) {
	case ".github/workflows/ci.yml", "internal/identity/domain/common_passwords.go":
		return true
	default:
		return false
	}
}

// termsSkipped reports paths the glossary check must not read. The word list
// and its test name the retired words, so they cannot be checked.
func termsSkipped(path string) bool {
	switch normalTermsPath(path) {
	case "web/pnpm-lock.yaml", "THIRD_PARTY_NOTICES.md",
		"tools/repolint/terms.go", "tools/repolint/terms_test.go":
		return true
	default:
		return false
	}
}

// termsChecked reports whether path is a file the glossary check reads.
func termsChecked(path string) bool {
	if filepath.Base(path) == "Makefile" {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".go", ".ts", ".tsx", ".css", ".html", ".json", ".yml", ".yaml", ".sh", ".sql":
		return true
	default:
		return false
	}
}

// scanTermsFile checks one path. checked is false when the path is skipped,
// missing, or not a regular file. A missing path is not an error.
func scanTermsFile(path string) (hits []string, checked bool, err error) {
	if termsSkipped(path) || !termsChecked(path) {
		return nil, false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	// Path is one line from git ls-files, the files this repository tracks.
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	for n, line := range strings.Split(string(data), "\n") {
		for _, word := range retiredInLine(line) {
			hits = append(hits, fmt.Sprintf("%s:%d: retired word %q", path, n+1, word))
		}
		if !britishSkipped(path) {
			for _, word := range britishInLine(line) {
				us := britishSpellings[strings.ToLower(word)]
				hits = append(hits, fmt.Sprintf("%s:%d: British spelling %q, use %q", path, n+1, word, us))
			}
		}
	}
	return hits, true, nil
}

func runTerms(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("terms", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		printf(stderr, "usage: git ls-files | repolint terms\n")
		return 2
	}
	sc := bufio.NewScanner(stdin)
	checked := 0
	failed := 0
	for sc.Scan() {
		path := strings.TrimRight(sc.Text(), "\r")
		if path == "" {
			continue
		}
		hits, ok, err := scanTermsFile(path)
		if err != nil {
			printf(stderr, "repolint: %s: %v\n", path, err)
			return 2
		}
		if !ok {
			continue
		}
		checked++
		if len(hits) > 0 {
			failed++
		}
		for _, hit := range hits {
			printf(stderr, "%s\n", hit)
		}
	}
	if err := sc.Err(); err != nil {
		printf(stderr, "repolint: %v\n", err)
		return 2
	}
	if failed > 0 {
		printf(stderr, "repolint: %d of %d files failed\n", failed, checked)
		return 1
	}
	printf(stdout, "repolint: %d files ok\n", checked)
	return 0
}
