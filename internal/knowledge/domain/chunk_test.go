package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// startOf returns the source offset of a chunk body. Fixture text carries
// unique counters, so the first occurrence is the true position.
func startOf(source string, chunk ChunkResult) int {
	return strings.Index(source, chunk.Body)
}

func TestChunkKeepsAShortSourceWhole(t *testing.T) {
	got, err := Chunk("Ada prefers oolong tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "Ada prefers oolong tea" || got[0].HeadingPath != "" {
		t.Fatalf("chunks = %#v", got)
	}
}

func TestChunkKeepsAShortSourcesHeadingPath(t *testing.T) {
	got, err := Chunk("# Guide\nAda prefers oolong tea.\n\n## Install\nAda installs oolong.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].HeadingPath != "Guide" {
		t.Fatalf("chunks = %#v", got)
	}
}

func TestChunkRejectsBlankText(t *testing.T) {
	_, err := Chunk("  \n\t")
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("err = %v", err)
	}
}

func TestChunkSlicesALongSourceWithoutRewritingIt(t *testing.T) {
	source := numberedSource(800)
	got, err := Chunk(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("chunks = %d", len(got))
	}
	maxBytes := MaxTokens * TokenBytes
	minBytes := MinTokens * TokenBytes
	cursor := 0
	prevEnd := 0
	for i, chunk := range got {
		at := strings.Index(source[cursor:], chunk.Body)
		if at < 0 {
			t.Fatalf("chunk %d is not a substring", i)
		}
		abs := cursor + at
		if !utf8.ValidString(chunk.Body) {
			t.Fatalf("chunk %d is not valid text", i)
		}
		if i < len(got)-1 && (len(chunk.Body) < minBytes || len(chunk.Body) > maxBytes) {
			t.Fatalf("chunk %d length %d", i, len(chunk.Body))
		}
		if i == len(got)-1 && (len(chunk.Body) == 0 || len(chunk.Body) > maxBytes) {
			t.Fatalf("tail length %d", len(chunk.Body))
		}
		if i > 0 {
			overlap := prevEnd - abs
			if overlap < 0 {
				t.Fatalf("chunk %d leaves a gap", i)
			}
			// The first overlap is pinned literally, not derived from the
			// constant, so a changed percent cannot pass unnoticed.
			if i == 1 && (overlap < 240 || overlap > 360) {
				t.Fatalf("chunk %d overlap %d, want about 300", i, overlap)
			}
			want := TargetTokens * TokenBytes * OverlapPercent / 100
			if overlap > want+400 {
				t.Fatalf("chunk %d overlap %d, want at most about %d", i, overlap, want)
			}
		}
		prevEnd = abs + len(chunk.Body)
		cursor = abs + 1
	}
}

// TestChunkPreservesContiguousVerbatimSource reconstructs the covered byte
// ranges from the returned bodies. Bodies must be exact source slices, the
// union must cover the whole source with no gap, and overlaps must stay
// small. No body may be trimmed or rebuilt.
func TestChunkPreservesContiguousVerbatimSource(t *testing.T) {
	source := mixedSource()
	got, err := Chunk(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("chunks = %d", len(got))
	}
	maxBytes := MaxTokens * TokenBytes
	minBytes := MinTokens * TokenBytes
	prevEnd := 0
	for i, chunk := range got {
		body := chunk.Body
		if body == "" || !utf8.ValidString(body) {
			t.Fatalf("chunk %d is empty or invalid", i)
		}
		// The start must sit at or before the previous end so no byte is
		// skipped, and the body must match the source byte for byte.
		start := -1
		for s := prevEnd; s >= 0; s-- {
			if s+len(body) <= len(source) && source[s:s+len(body)] == body {
				start = s
				break
			}
		}
		if start < 0 {
			t.Fatalf("chunk %d is not an exact source slice", i)
		}
		if i > 0 && prevEnd-start > TargetTokens*TokenBytes*OverlapPercent/100+400 {
			t.Fatalf("chunk %d overlaps %d bytes", i, prevEnd-start)
		}
		if i < len(got)-1 && (len(body) < minBytes || len(body) > maxBytes) {
			t.Fatalf("chunk %d length %d", i, len(body))
		}
		prevEnd = start + len(body)
	}
	if !strings.HasSuffix(source, got[len(got)-1].Body) {
		t.Fatal("the last chunk does not reach the end of the source")
	}
}

// TestChunkUsesHeadingBoundariesAndPath pins section cuts at every eligible
// heading and the heading path of each chunk.
func TestChunkUsesHeadingBoundariesAndPath(t *testing.T) {
	source := "# Guide\n" + para(1712, "intro") + "\n" +
		"## Install\n" + para(1708, "install") + "\n" +
		"### Linux\n" + para(1700, "linux") + "\n" +
		"### Mac\n" + para(1708, "mac") + "\n" +
		"## Troubleshoot\n" + para(2400, "fix")
	got, err := Chunk(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"## Install\n", "### Linux\n", "### Mac\n", "## Troubleshoot\n"} {
		at := strings.Index(source, h)
		found := false
		for _, chunk := range got {
			if startOf(source, chunk)+len(chunk.Body) == at {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no chunk ends at heading %q", strings.TrimSpace(h))
		}
	}
	wantPaths := []string{
		"Guide",
		"Guide",
		"Guide > Install",
		"Guide > Install > Linux",
		"Guide > Install > Mac",
		"Guide > Troubleshoot",
	}
	if len(got) != len(wantPaths) {
		t.Fatalf("chunks = %d, want %d: %q", len(got), len(wantPaths), pathsOf(got))
	}
	for i, want := range wantPaths {
		if got[i].HeadingPath != want {
			t.Fatalf("chunk %d path = %q, want %q", i, got[i].HeadingPath, want)
		}
	}
}

func pathsOf(chunks []ChunkResult) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.HeadingPath
	}
	return out
}

// TestChunkMergesAShortTailOnlyWhenItFits pins the final tail rule: a tail
// that fits within 600 tokens with its predecessor is one chunk, and a tail
// whose backward merge would exceed 600 stays a separate short chunk.
func TestChunkMergesAShortTailOnlyWhenItFits(t *testing.T) {
	minBytes := MinTokens * TokenBytes
	t.Run("tail merges into the final chunk when it fits", func(t *testing.T) {
		source := para(1900, "first") + "\n\n" + para(1900, "second") + "\n\n" + para(150, "third")
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("chunks = %d, want 2: sizes %v", len(got), sizes(got))
		}
		for i, chunk := range got {
			if len(chunk.Body) < minBytes {
				t.Fatalf("chunk %d of %d bytes is a short tail", i, len(chunk.Body))
			}
		}
		if !strings.Contains(got[1].Body, "third") {
			t.Fatal("the tail text is not inside the final chunk")
		}
	})
	t.Run("tail stays short when the merge would exceed the maximum", func(t *testing.T) {
		source := para(1900, "first") + "\n\n" + para(1900, "second") + "\n\n" + para(399, "third")
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("chunks = %d, want 3: sizes %v", len(got), sizes(got))
		}
		tail := len(got[2].Body)
		if tail >= minBytes {
			t.Fatalf("tail = %d bytes, want a short one", tail)
		}
		prev := startOf(source, got[1])
		if len(source)-prev <= MaxTokens*TokenBytes {
			t.Fatal("the merge would fit; the fixture no longer pins the refusal")
		}
	})
}

func sizes(chunks []ChunkResult) []int {
	out := make([]int, len(chunks))
	for i, c := range chunks {
		out[i] = len(c.Body)
	}
	return out
}

// TestChunkPrefersANearerBoundaryOverAReachableHeading pins that a heading
// caps the cut window but still loses to a boundary nearer the target.
func TestChunkPrefersANearerBoundaryOverAReachableHeading(t *testing.T) {
	first := para(1990, "first")
	source := first + "\n\n" + para(300, "second") + "\n" + "## Later\n" + para(1700, "later")
	got, err := Chunk(source)
	if err != nil {
		t.Fatal(err)
	}
	at := len(first) + 2
	if got[0].Body != source[:at] {
		t.Fatalf("chunk 1 ends at %d, want the paragraph boundary at %d (heading is at %d)",
			len(got[0].Body), at, strings.Index(source, "## Later"))
	}
}

// TestChunkUsesParagraphSentenceAndWhitespaceFallback pins that plain
// paragraphs cut at paragraph boundaries, sentence-heavy paragraphs at
// sentence ends, and punctuation-free text at whitespace.
func TestChunkUsesParagraphSentenceAndWhitespaceFallback(t *testing.T) {
	t.Run("paragraph boundary", func(t *testing.T) {
		a, b := para(1900, "first"), para(1900, "second")
		source := a + "\n\n" + b
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("chunks = %d, want 2", len(got))
		}
		at := len(a) + 2
		if got[0].Body != source[:at] {
			t.Fatalf("first cut not at the paragraph boundary: %d vs %d", len(got[0].Body), at)
		}
		start := startOf(source, got[1])
		if at-start < 240 || at-start > 360 {
			t.Fatalf("second chunk starts %d bytes before the boundary, want about 300", at-start)
		}
		if source[start-1] != '.' || source[start] != ' ' {
			t.Fatal("the overlap does not start at a sentence end")
		}
	})
	t.Run("sentence boundary", func(t *testing.T) {
		source := strings.Repeat("The kettle holds tea for the team. ", 140)
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) < 2 {
			t.Fatalf("chunks = %d", len(got))
		}
		for i, chunk := range got {
			end := startOf(source, chunk) + len(chunk.Body)
			if i < len(got)-1 && (source[end-1] != '.' || source[end] != ' ') {
				t.Fatalf("chunk %d does not cut at a sentence end", i)
			}
		}
	})
	t.Run("whitespace boundary", func(t *testing.T) {
		source := strings.Repeat("wo rd ", 900)
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) < 2 {
			t.Fatalf("chunks = %d", len(got))
		}
		for i, chunk := range got {
			end := startOf(source, chunk) + len(chunk.Body)
			if i < len(got)-1 {
				c := source[end-1]
				if c != ' ' && c != '\n' && c != '.' && c != '!' && c != '?' {
					t.Fatalf("chunk %d cuts mid-word at %q", i, c)
				}
			}
		}
	})
}

// TestChunkKeepsTablesWholeAndAllowsOversizedTable pins that a fitting table
// is never split and that a table above the maximum is the one exception.
func TestChunkKeepsTablesWholeAndAllowsOversizedTable(t *testing.T) {
	t.Run("fitting table stays whole", func(t *testing.T) {
		table := fittingTable()
		// The table straddles the first cut window, so without table
		// detection a whitespace cut would land inside it.
		source := para(1500, "intro") + "\n" + table + para(800, "outro")
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		tableStart := strings.Index(source, "| name |")
		tableEnd := tableStart + len(table)
		whole := false
		for _, chunk := range got {
			if strings.Contains(chunk.Body, "| name |") {
				if !strings.Contains(chunk.Body, table) {
					t.Fatal("the table is split across chunks")
				}
				whole = true
			}
		}
		if !whole {
			t.Fatal("no chunk carries the table")
		}
		for _, chunk := range got {
			start := startOf(source, chunk)
			end := start + len(chunk.Body)
			// A chunk may contain the table whole; only one boundary
			// strictly inside the table range splits it.
			if (start > tableStart && start < tableEnd) || (end > tableStart && end < tableEnd) {
				t.Fatalf("a boundary falls inside the table at %d..%d", start, end)
			}
		}
	})
	t.Run("oversized table is one chunk", func(t *testing.T) {
		intro := strings.Repeat("i", 99) + "\n"
		table := oversizedTable()
		outro := strings.Repeat("o", 400)
		source := intro + table + outro
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("chunks = %d, want 3", len(got))
		}
		if got[0].Body != intro || got[1].Body != table || got[2].Body != outro {
			t.Fatalf("oversized table chunks = [%d %d %d]",
				len(got[0].Body), len(got[1].Body), len(got[2].Body))
		}
	})
	t.Run("header and delimiter alone is a table", func(t *testing.T) {
		table := "| only | header |\n| --- | --- |\n"
		source := para(1800, "intro") + "\n" + table + para(800, "outro")
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		tableStart := strings.Index(source, "| only |")
		tableEnd := tableStart + len(table)
		endsAtTableEnd := false
		for _, chunk := range got {
			start := startOf(source, chunk)
			end := start + len(chunk.Body)
			if end == tableEnd {
				endsAtTableEnd = true
			}
			if (start > tableStart && start < tableEnd) || (end > tableStart && end < tableEnd) {
				t.Fatalf("a boundary falls inside the table at %d..%d", start, end)
			}
		}
		if !endsAtTableEnd {
			t.Fatal("no chunk ends at the header-only table end")
		}
	})
	t.Run("the overlap never starts inside a table", func(t *testing.T) {
		// The table sits whole inside the first chunk with the 15% overlap
		// point landing inside it, so the next chunk must start at the table
		// start or at the previous end, never inside.
		table := "| one | two |\n| --- | --- |\n" + strings.Repeat("| aaa | bbb |\n", 28)
		source := para(1700, "intro") + "\n" + table + para(1800, "tail")
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("chunks = %d, want 2", len(got))
		}
		tableStart := strings.Index(source, "| one |")
		tableEnd := tableStart + len(table)
		for i, chunk := range got {
			start := startOf(source, chunk)
			end := start + len(chunk.Body)
			if (start > tableStart && start < tableEnd) || (end > tableStart && end < tableEnd) {
				t.Fatalf("chunk %d boundary falls inside the table at %d..%d", i, start, end)
			}
		}
		if start := startOf(source, got[1]); start != tableStart {
			t.Fatalf("second chunk starts at %d, want the table start at %d", start, tableStart)
		}
		if !strings.Contains(got[1].Body, table) {
			t.Fatal("the second chunk does not carry the whole table")
		}
	})
}

// TestChunkTreatsCodeFenceAsAtomicAndSplitsListsBetweenItems pins that
// nothing inside a fence is parsed, an oversized fence cuts at line ends,
// and an oversized list cuts between items.
func TestChunkTreatsCodeFenceAsAtomicAndSplitsListsBetweenItems(t *testing.T) {
	t.Run("fitting fence is atomic and unparsed", func(t *testing.T) {
		fence := "```go\n# Not a heading\n| a | b |\n| --- |\n| c | d |\n- not a list\n" +
			strings.Repeat("plain code line\n", 30) + "```\n"
		source := para(1600, "intro") + "\n" + fence + para(1800, "outro")
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		fenceStart := strings.Index(source, "```go")
		fenceEnd := fenceStart + len(fence)
		for _, chunk := range got {
			if strings.Contains(chunk.HeadingPath, "Not a heading") {
				t.Fatalf("fence content became a heading path: %q", chunk.HeadingPath)
			}
			start := startOf(source, chunk)
			end := start + len(chunk.Body)
			if (start > fenceStart && start < fenceEnd) || (end > fenceStart && end < fenceEnd) {
				t.Fatalf("boundary %d..%d falls inside the fence %d..%d", start, end, fenceStart, fenceEnd)
			}
		}
	})
	t.Run("oversized fence cuts at line ends", func(t *testing.T) {
		intro := strings.Repeat("i", 59) + "\n"
		line := "// done. next call " + strings.Repeat("done. next call ", 4) + "cc\n"
		// Four backticks open a fence too; an odd count would mask the cut
		// class through the inline-code parity guard.
		fence := "````go\n" + strings.Repeat(line, 30) + "````\n"
		source := intro + fence
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) < 2 {
			t.Fatalf("chunks = %d", len(got))
		}
		// The lines repeat, so locate each body by searching backward from
		// the previous end; Index would find a ghost copy.
		prevEnd := 0
		for i, chunk := range got {
			start := -1
			for s := prevEnd; s >= 0; s-- {
				if s+len(chunk.Body) <= len(source) && source[s:s+len(chunk.Body)] == chunk.Body {
					start = s
					break
				}
			}
			if start < 0 {
				t.Fatalf("chunk %d is not an exact source slice", i)
			}
			end := start + len(chunk.Body)
			if i < len(got)-1 && source[end-1] != '\n' {
				t.Fatalf("chunk %d does not cut at a code line end", i)
			}
			prevEnd = end
		}
	})
	t.Run("oversized list cuts between items", func(t *testing.T) {
		source := para(100, "intro") + "\n" + listItems(12, 350) + para(100, "outro")
		got, err := Chunk(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) < 2 {
			t.Fatalf("chunks = %d", len(got))
		}
		outroStart := strings.Index(source, "outro")
		for i, chunk := range got {
			start := startOf(source, chunk)
			end := start + len(chunk.Body)
			if i > 0 && (source[start] != '-' || source[start+1] != ' ') {
				t.Fatalf("chunk %d starts at %d inside an item", i, start)
			}
			if i < len(got)-1 && end != outroStart && (source[end] != '-' || source[end+1] != ' ') {
				t.Fatalf("chunk %d ends at %d inside an item", i, end)
			}
		}
	})
}

func TestAlignLandsOnARuneStart(t *testing.T) {
	s := "x" + "日" + "y" // x, then a 3-byte rune at 1..4, then y
	cases := []struct {
		i, hi, want int
	}{
		{2, 3, 1},  // i and hi both inside the rune: back to its start
		{2, 10, 4}, // forward to the next start
		{0, 3, 0},  // already at a start: stays
		{3, 3, 1},  // hi inside the rune, i beyond a start: back
		{4, 4, 4},  // hi on a start: stays
	}
	for _, c := range cases {
		if got := align(s, c.i, c.hi); got != c.want {
			t.Fatalf("align(%d, %d) = %d, want %d", c.i, c.hi, got, c.want)
		}
		if c.want < len(s) && !utf8.RuneStart(s[c.want]) {
			t.Fatalf("align(%d, %d) landed mid-rune at %d", c.i, c.hi, c.want)
		}
	}
}

func TestChunkDoesNotSplitARune(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&b, "café%04d ", i)
	}
	source := b.String()
	got, err := Chunk(source)
	if err != nil {
		t.Fatal(err)
	}
	for i, chunk := range got {
		if !utf8.ValidString(chunk.Body) {
			t.Fatalf("chunk %d split a rune", i)
		}
		if !strings.Contains(source, chunk.Body) {
			t.Fatalf("chunk %d is not verbatim", i)
		}
	}
	// No whitespace at all: the hard cut must still land on a rune boundary.
	unspaced := strings.Repeat("café日本語", 300)
	got, err = Chunk(unspaced)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("chunks = %d", len(got))
	}
	for i, chunk := range got {
		if !utf8.ValidString(chunk.Body) {
			t.Fatalf("unspaced chunk %d split a rune", i)
		}
		if !strings.Contains(unspaced, chunk.Body) {
			t.Fatalf("unspaced chunk %d is not verbatim", i)
		}
	}
}

func para(n int, tag string) string {
	var b strings.Builder
	i := 0
	for b.Len() < n {
		fmt.Fprintf(&b, "%s words about tea %04d. ", tag, i)
		i++
	}
	return b.String()[:n]
}

func numberedSource(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "w%04d ", i)
	}
	return b.String()
}

func fittingTable() string {
	rows := []string{
		"| name | notes |",
		"| --- | --- |",
		"| a \\| b | escaped pipe |",
		"| `x|y` | inline code pipe |",
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r)
		b.WriteString("\n")
	}
	for i := 0; i < 18; i++ {
		b.WriteString("| row ")
		b.WriteString(strings.Repeat("z", 20))
		b.WriteString(" | more text here |\n")
	}
	return b.String()
}

func oversizedTable() string {
	var b strings.Builder
	b.WriteString("|" + strings.Repeat("h", 97) + "|\n")
	b.WriteString("|" + strings.Repeat("-", 97) + "|\n")
	for i := 0; i < 48; i++ {
		b.WriteString("|" + strings.Repeat("r", 97) + "|\n")
	}
	return b.String()
}

func listItems(count, size int) string {
	var b strings.Builder
	for i := 0; i < count; i++ {
		item := fmt.Sprintf("- item %02d ", i)
		item += strings.Repeat("t ", (size-len(item)-1)/2)
		b.WriteString(item)
		b.WriteString("\n")
	}
	return b.String()
}

func mixedSource() string {
	var b strings.Builder
	b.WriteString("# Guide café ünïcode\n")
	b.WriteString(para(1700, "café intro") + "\n")
	b.WriteString("## Install\n")
	b.WriteString(para(500, "plain") + "\n")
	b.WriteString("| h1 | h2 |\n| --- | --- |\n| a | b |\n")
	b.WriteString(para(1700, "after") + "\n")
	b.WriteString(listItems(3, 300))
	b.WriteString("```\nnot | a | table heading\n```\n")
	b.WriteString(para(1700, "tail"))
	return b.String()
}
