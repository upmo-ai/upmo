package main

import (
	"bufio"
	"encoding/xml"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxFailedNames = 20
	logTailLines   = 40
	maxLineLength  = 300
)

// stringList collects a repeated flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// runSummary writes a Markdown summary of one CI job to stdout. Each -file is
// read by its extension: .xml is JUnit, .cover is a Go coverage profile, .info
// is lcov and .log is job output. A file that is missing is named in the
// summary, never skipped silently. The log tail shows only for a job that did
// not succeed. The command exits 0 even when the job failed: it reports, the
// job status decides the result.
func runSummary(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("summary", flag.ContinueOnError)
	fset.SetOutput(stderr)
	title := fset.String("title", "", "job name")
	status := fset.String("status", "", "job status: success, failure or canceled")
	var files stringList
	fset.Var(&files, "file", "report file to read (repeatable)")
	if err := fset.Parse(args); err != nil {
		return 2
	}
	if *title == "" || *status == "" || fset.NArg() != 0 {
		printf(stderr, "usage: repolint summary -title name -status status [-file path]...\n")
		return 2
	}
	printf(stdout, "## %s: %s\n\n", *title, statusWord(*status))
	for _, f := range files {
		writeReport(stdout, f, *status == "success")
	}
	return 0
}

func statusWord(status string) string {
	switch status {
	case "success":
		return "passed"
	case "failure":
		return "failed"
	default:
		return status
	}
}

func writeReport(w io.Writer, path string, passed bool) {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		printf(w, "- `%s`: not found, the job stopped before writing it.\n", path)
		return
	}
	if err != nil {
		printf(w, "- `%s`: unreadable (%v).\n", path, err)
		return
	}
	switch filepath.Ext(path) {
	case ".xml":
		writeJUnit(w, path, data)
	case ".cover":
		writeCoverage(w, path, goCoverage(string(data)), "statements")
	case ".info":
		writeCoverage(w, path, lcovCoverage(string(data)), "lines")
	default:
		if !passed {
			writeLogTail(w, path, string(data))
		}
	}
}

type junitCase struct {
	Name      string    `xml:"name,attr"`
	Classname string    `xml:"classname,attr"`
	Failure   *struct{} `xml:"failure"`
	Error     *struct{} `xml:"error"`
	Skipped   *struct{} `xml:"skipped"`
}

type junitSuite struct {
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}

type junitRoot struct {
	Suites []junitSuite `xml:"testsuite"`
	Cases  []junitCase  `xml:"testcase"`
}

func (s junitSuite) walk(visit func(junitCase)) {
	for _, c := range s.Cases {
		visit(c)
	}
	for _, child := range s.Suites {
		child.walk(visit)
	}
}

// writeJUnit counts tests and names the failed ones. It accepts both a
// <testsuites> and a bare <testsuite> root.
func writeJUnit(w io.Writer, path string, data []byte) {
	var root junitRoot
	// The root element name is not matched, so a bare <testsuite> root fills
	// Cases and Suites the same way a <testsuites> root does.
	if err := xml.Unmarshal(data, &root); err != nil {
		printf(w, "- `%s`: not valid JUnit (%v).\n", path, err)
		return
	}
	var total, failed, skipped int
	var names []string
	visit := func(c junitCase) {
		total++
		switch {
		case c.Failure != nil || c.Error != nil:
			failed++
			names = append(names, strings.TrimSpace(c.Classname+" "+c.Name))
		case c.Skipped != nil:
			skipped++
		}
	}
	for _, c := range root.Cases {
		visit(c)
	}
	for _, s := range root.Suites {
		s.walk(visit)
	}
	printf(w, "- `%s`: %d tests, %d passed, %d failed, %d skipped.\n", path, total, total-failed-skipped, failed, skipped)
	for i, n := range names {
		if i == maxFailedNames {
			printf(w, "  - and %d more\n", len(names)-maxFailedNames)
			break
		}
		printf(w, "  - failed: `%s`\n", clip(n))
	}
}

func writeCoverage(w io.Writer, path string, pct float64, unit string) {
	if pct < 0 {
		printf(w, "- `%s`: no coverage data.\n", path)
		return
	}
	printf(w, "- `%s`: %.1f%% of %s covered.\n", path, pct, unit)
}

// goCoverage returns the statement coverage of a Go profile, or -1 when the
// profile has no statements. Lines look like name.go:1.2,3.4 numStmts count.
func goCoverage(profile string) float64 {
	var total, covered int
	sc := bufio.NewScanner(strings.NewReader(profile))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 3 || strings.HasPrefix(sc.Text(), "mode:") {
			continue
		}
		stmts, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			continue
		}
		total += stmts
		if count > 0 {
			covered += stmts
		}
	}
	if total == 0 {
		return -1
	}
	return float64(covered) * 100 / float64(total)
}

// lcovCoverage returns line coverage from LF (lines found) and LH (lines hit)
// records, or -1 when there are no lines.
func lcovCoverage(info string) float64 {
	var found, hit int
	for _, line := range strings.Split(info, "\n") {
		switch {
		case strings.HasPrefix(line, "LF:"):
			n, _ := strconv.Atoi(strings.TrimSpace(line[3:]))
			found += n
		case strings.HasPrefix(line, "LH:"):
			n, _ := strconv.Atoi(strings.TrimSpace(line[3:]))
			hit += n
		}
	}
	if found == 0 {
		return -1
	}
	return float64(hit) * 100 / float64(found)
}

// writeLogTail prints the last lines of a failed job's output in a fence that
// the output itself cannot close.
func writeLogTail(w io.Writer, path, log string) {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	if len(lines) > logTailLines {
		lines = lines[len(lines)-logTailLines:]
	}
	printf(w, "- `%s`: last %d lines.\n\n````text\n", path, len(lines))
	for _, l := range lines {
		printf(w, "%s\n", strings.ReplaceAll(clip(l), "````", "'''"))
	}
	printf(w, "````\n")
}

func clip(s string) string {
	if r := []rune(s); len(r) > maxLineLength {
		return string(r[:maxLineLength]) + "..."
	}
	return s
}
