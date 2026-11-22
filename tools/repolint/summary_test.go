package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const junitFixture = `<?xml version="1.0"?>
<testsuites>
  <testsuite name="a" tests="4">
    <testcase classname="pkg/a" name="TestOk"/>
    <testcase classname="pkg/a" name="TestBad"><failure message="boom"/></testcase>
    <testcase classname="pkg/a" name="TestErr"><error message="boom"/></testcase>
    <testcase classname="pkg/a" name="TestSkip"><skipped/></testcase>
  </testsuite>
</testsuites>`

const goProfile = `mode: atomic
a.go:1.1,2.2 4 1
a.go:3.1,4.2 6 0
`

const lcovInfo = "SF:a.ts\nLF:10\nLH:7\nend_of_record\nSF:b.ts\nLF:10\nLH:10\nend_of_record\n"

func writeFile(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunSummary(t *testing.T) {
	dir := t.TempDir()
	junit := writeFile(t, dir, "t.xml", junitFixture)
	cover := writeFile(t, dir, "go.cover", goProfile)
	lcov := writeFile(t, dir, "lcov.info", lcovInfo)
	log := writeFile(t, dir, "x.log", "one\ntwo\nFAIL here\n")
	missing := filepath.Join(dir, "gone.xml")

	var out, errOut bytes.Buffer
	args := []string{"-title", "Go Tests", "-status", "failure",
		"-file", junit, "-file", cover, "-file", lcov, "-file", log, "-file", missing}
	if code := runSummary(args, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"## Go Tests: failed\n",
		"4 tests, 1 passed, 2 failed, 1 skipped.",
		"failed: `pkg/a TestBad`",
		"failed: `pkg/a TestErr`",
		"40.0% of statements covered.",
		"85.0% of lines covered.",
		"FAIL here\n````",
		"not found, the job stopped before writing it.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestRunSummaryPassedHidesLog(t *testing.T) {
	dir := t.TempDir()
	log := writeFile(t, dir, "x.log", "noise\n")
	var out, errOut bytes.Buffer
	if code := runSummary([]string{"-title", "Lint", "-status", "success", "-file", log}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := out.String(); got != "## Lint: passed\n\n" {
		t.Errorf("got %q", got)
	}
}

func TestRunSummaryLimits(t *testing.T) {
	dir := t.TempDir()
	var cases strings.Builder
	for i := 0; i < 25; i++ {
		cases.WriteString(`<testcase classname="p" name="T"><failure/></testcase>`)
	}
	junit := writeFile(t, dir, "t.xml", "<testsuite>"+cases.String()+"</testsuite>")
	var lines strings.Builder
	for i := 0; i < 100; i++ {
		lines.WriteString("line\n")
	}
	lines.WriteString(strings.Repeat("x", 500) + "\n````\n")
	log := writeFile(t, dir, "x.log", lines.String())

	var out, errOut bytes.Buffer
	runSummary([]string{"-title", "T", "-status", "failure", "-file", junit, "-file", log}, &out, &errOut)
	got := out.String()
	if !strings.Contains(got, "25 tests, 0 passed, 25 failed, 0 skipped.") || !strings.Contains(got, "and 5 more") {
		t.Errorf("failed names not capped:\n%s", got)
	}
	if !strings.Contains(got, "last 40 lines.") || strings.Count(got, "````") != 2 {
		t.Errorf("log tail not bounded or fence not safe:\n%s", got)
	}
	if strings.Contains(got, strings.Repeat("x", 301)) {
		t.Error("long line not clipped")
	}
}

func TestRunSummaryUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"-title", "T"}, {"-title", "T", "-status", "success", "extra"}} {
		var out, errOut bytes.Buffer
		if code := runSummary(args, &out, &errOut); code != 2 {
			t.Errorf("args %v: exit %d, want 2", args, code)
		}
	}
}

func TestRunSummaryBadJUnit(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "t.xml", "<testsuite><oops>")
	var out, errOut bytes.Buffer
	runSummary([]string{"-title", "T", "-status", "failure", "-file", bad}, &out, &errOut)
	if !strings.Contains(out.String(), "not valid JUnit") {
		t.Errorf("got %q", out.String())
	}
}
