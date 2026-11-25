package main

import (
	"fmt"
	"strings"
)

// localBuildImages are compose image tags produced by a build in this
// repository. They are not pulled, so they carry no registry digest.
var localBuildImages = map[string]bool{
	"upmo/sandbox:base":   true,
	"upmo/markitdown:dev": true,
}

// unpinnedImage returns the first pulled image reference in data that has
// no @sha256: digest, naming that line. filename is the path in the message.
// A compose image tag in localBuildImages is exempt. A Dockerfile FROM that
// names a stage declared earlier in the file is exempt.
func unpinnedImage(filename string, data []byte) string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	stages := map[string]bool{}
	for i, line := range lines {
		n := i + 1
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# syntax=") {
			if !hasDigest(trimmed) {
				return pinFailure(filename, n, trimmed)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		body := stripInlineComment(line)
		if ref, ok := imageValue(body); ok {
			if localBuildImages[ref] || hasDigest(ref) {
				continue
			}
			return pinFailure(filename, n, strings.TrimSpace(body))
		}
		ref, stage, ok := fromValue(body)
		if !ok {
			continue
		}
		if !stages[ref] && !hasDigest(ref) {
			return pinFailure(filename, n, strings.TrimSpace(body))
		}
		if stage != "" {
			stages[stage] = true
		}
	}
	return ""
}

func pinFailure(filename string, n int, line string) string {
	return fmt.Sprintf("%s:%d: %s lacks @sha256:", filename, n, line)
}

func hasDigest(ref string) bool {
	return strings.Contains(ref, "@sha256:")
}

func stripInlineComment(line string) string {
	if i := strings.Index(line, " #"); i >= 0 {
		return line[:i]
	}
	return line
}

func imageValue(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(trimmed, "image:")
	if !ok {
		return "", false
	}
	// An empty or block scalar value is reported, not skipped: the check reads one line.
	return strings.Trim(strings.TrimSpace(rest), `"'`), true
}

func fromValue(line string) (ref, stage string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
		return "", "", false
	}
	args := fields[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", "", true
	}
	ref = args[0]
	if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
		stage = args[2]
	}
	return ref, stage, true
}
