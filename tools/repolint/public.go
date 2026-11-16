package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const agplBanner = "GNU AFFERO GENERAL PUBLIC LICENSE"

var requiredGitignoreEntries = []string{
	".env",
	".env.*",
	"!.env.example",
	"AGENTS.md",
	"CLAUDE.md",
	".claude/",
	".cursor/",
	".kiro/",
	".gemini/",
}

var forbiddenAgentDirs = map[string]bool{
	".claude": true,
	".cursor": true,
	".kiro":   true,
	".gemini": true,
}

// readOptionalFile reads rel under dir, returning exists=false when missing.
func readOptionalFile(dir, rel string) (data []byte, exists bool, err error) {
	// Callers pass fixed repository filenames relative to dir.
	data, err = os.ReadFile(filepath.Join(dir, rel)) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

func checkLicense(dir string) ([]string, error) {
	data, ok, err := readOptionalFile(dir, "LICENSE")
	if err != nil {
		return nil, err
	}
	if !ok {
		return []string{"LICENSE is missing"}, nil
	}
	if !strings.Contains(string(data), agplBanner) {
		return []string{fmt.Sprintf("LICENSE must contain %q", agplBanner)}, nil
	}
	return nil, nil
}

func checkNonEmptyFile(dir, rel string) ([]string, error) {
	data, ok, err := readOptionalFile(dir, rel)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []string{fmt.Sprintf("%s is missing", rel)}, nil
	}
	if strings.TrimSpace(string(data)) == "" {
		return []string{fmt.Sprintf("%s is empty", rel)}, nil
	}
	return nil, nil
}

func parseGitignoreEntries(data []byte) map[string]bool {
	entries := make(map[string]bool)
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entries[trimmed] = true
	}
	return entries
}

func checkGitignore(dir string) ([]string, error) {
	data, ok, err := readOptionalFile(dir, ".gitignore")
	if err != nil {
		return nil, err
	}
	if !ok {
		return []string{".gitignore is missing"}, nil
	}
	entries := parseGitignoreEntries(data)
	var failures []string
	for _, req := range requiredGitignoreEntries {
		if !entries[req] {
			failures = append(failures, fmt.Sprintf(".gitignore is missing required entry %q", req))
		}
	}
	return failures, nil
}

// isForbiddenTrackedPath reports whether a tracked path is a secret env file
// or local agent rule/directory that must never be committed.
func isForbiddenTrackedPath(path string) bool {
	norm := strings.TrimPrefix(filepath.ToSlash(path), "./")
	base := filepath.Base(norm)
	if base == ".env" || (strings.HasPrefix(base, ".env.") && base != ".env.example") {
		return true
	}
	if base == "AGENTS.md" || base == "CLAUDE.md" {
		return true
	}
	for seg := range strings.SplitSeq(norm, "/") {
		if forbiddenAgentDirs[seg] {
			return true
		}
	}
	return false
}

func checkTrackedFiles(dir string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil
	}
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files: %w: %s", err, strings.TrimSpace(errOut.String()))
	}
	var failures []string
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		path := sc.Text()
		if isForbiddenTrackedPath(path) {
			failures = append(failures, fmt.Sprintf("forbidden file is tracked in git: %s", path))
		}
	}
	return failures, sc.Err()
}

// checkPublic verifies the going-public invariants for the repository at dir.
func checkPublic(dir string) ([]string, error) {
	var failures []string

	f, err := checkLicense(dir)
	if err != nil {
		return nil, err
	}
	failures = append(failures, f...)

	for _, rel := range []string{"SECURITY.md", "CONTRIBUTING.md"} {
		f, err := checkNonEmptyFile(dir, rel)
		if err != nil {
			return nil, err
		}
		failures = append(failures, f...)
	}

	f, err = checkGitignore(dir)
	if err != nil {
		return nil, err
	}
	failures = append(failures, f...)

	f, err = checkTrackedFiles(dir)
	if err != nil {
		return nil, err
	}
	failures = append(failures, f...)

	return failures, nil
}

func runPublic(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("public", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", "repository root directory to check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		printf(stderr, "usage: repolint public [-dir path]\n")
		return 2
	}
	failures, err := checkPublic(*dir)
	if err != nil {
		printf(stderr, "repolint: %v\n", err)
		return 2
	}
	for _, msg := range failures {
		printf(stderr, "repolint: %s\n", msg)
	}
	if len(failures) > 0 {
		printf(stderr, "repolint: %d going-public checks failed\n", len(failures))
		return 1
	}
	printf(stdout, "repolint: going-public checks ok\n")
	return 0
}
