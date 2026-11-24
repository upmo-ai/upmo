package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MinTokens and MaxTokens bound a chunk of a long source. A short source
	// stays one chunk, because the text is not padded or merged.
	MinTokens = 400
	MaxTokens = 600
	// TargetTokens is the size the cutter aims for inside that range.
	TargetTokens = 500
	// TokenBytes is the profile budget's unit: four bytes are one token.
	TokenBytes = 4
	// OverlapPercent is how much of the target the next chunk repeats.
	OverlapPercent = 15
)

// ErrEmpty is a source with no text to store.
var ErrEmpty = errors.New("knowledge: empty source")

// ChunkResult is one verbatim slice and its heading path metadata.
type ChunkResult struct {
	Body        string
	HeadingPath string
}

// Chunk slices source into structured verbatim pieces. Every body is an
// unchanged contiguous slice of source; only the heading path is metadata.
func Chunk(source string) ([]ChunkResult, error) {
	if strings.TrimSpace(source) == "" {
		return nil, ErrEmpty
	}
	blocks := parseBlocks(source)
	markers := headingMarkers(blocks)
	if estTokens(source) <= MaxTokens {
		return []ChunkResult{{
			Body:        source,
			HeadingPath: headingPathAt(firstNonblank(source, 0, len(source)), markers),
		}}, nil
	}
	bounds := assemble(source, blocks)
	out := make([]ChunkResult, 0, len(bounds))
	for i, b := range bounds {
		if b.start < 0 || b.end > len(source) || b.start >= b.end {
			return nil, fmt.Errorf("knowledge: chunk did not advance")
		}
		if i > 0 && b.start <= bounds[i-1].start {
			return nil, fmt.Errorf("knowledge: chunk did not advance")
		}
		out = append(out, ChunkResult{
			Body:        source[b.start:b.end],
			HeadingPath: headingPathAt(firstNonblank(source, b.start, b.end), markers),
		})
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

func estTokens(s string) int {
	return (len(s) + TokenBytes - 1) / TokenBytes
}

type byteRange struct {
	start, end int
}

// blockKind orders the scan: fenced code wins over every other construct,
// then headings, tables, list items and paragraphs.
type blockKind int

const (
	blockParagraph blockKind = iota
	blockFence
	blockHeading
	blockTable
	blockItem
)

// block is one Markdown construct. Blocks partition the source: each block
// starts where the previous one ends and blank lines attach to the block
// before them, so chunk boundaries at block starts never leave a gap.
type block struct {
	start, end int
	kind       blockKind
	level      int    // heading level, 0 for other kinds
	title      string // heading title without the ATX marker
}

// cut is a byte offset where a chunk may end or begin. Class 2 is a block
// boundary, class 1 a sentence or line end inside an oversized block, class 0
// any other whitespace boundary.
type cut struct {
	pos   int
	class int
}

// assemble walks block boundaries and emits chunk ranges. A range is 400 to
// 600 estimated tokens unless a single Markdown table forces the documented
// exception. Adjacent ranges overlap by roughly 15% of the target.
func assemble(source string, blocks []block) []byteRange {
	target := TargetTokens * TokenBytes
	minBytes := MinTokens * TokenBytes
	maxBytes := MaxTokens * TokenBytes
	overlap := target * OverlapPercent / 100
	cuts := buildCuts(source, blocks)
	var headings []int
	for _, b := range blocks {
		if b.kind == blockHeading {
			headings = append(headings, b.start)
		}
	}
	var ranges []byteRange
	cur := 0
	for cur < len(source) {
		if len(source)-cur <= maxBytes {
			// Stopping at the first start inside the maximum is itself the
			// spec's tail rule: a predecessor plus tail that fits within
			// 600 tokens collapses into this one final chunk.
			ranges = append(ranges, byteRange{cur, len(source)})
			break
		}
		flush := false
		// A heading the chunk could legally stop at caps the window: the
		// chunk never spans an in-reach heading. The heading itself stays a
		// candidate and loses to a nearer boundary.
		hi := cur + maxBytes
		for _, h := range headings {
			if h >= cur+minBytes {
				if h < hi {
					hi = h
				}
				break
			}
		}
		end := pickCut(cuts, cur+target, cur+minBytes, hi)
		if end <= cur {
			// No boundary inside the window: one block spans it.
			w, ok := coveringBlock(blocks, cur+minBytes, cur+maxBytes)
			if !ok {
				w = blockAt(blocks, cur)
			}
			if w.kind == blockTable {
				if w.start > cur {
					end = w.start // flush before the table, however short
					flush = true
				} else {
					end = w.end // the one over-600 exception
					flush = true
				}
			} else {
				end = fallbackSplit(source, w.start, cur+minBytes, cur+maxBytes, cur+target, w.kind)
			}
		}
		if end <= cur || end > len(source) {
			return nil
		}
		ranges = append(ranges, byteRange{cur, end})
		if flush {
			// Re-entering a flushed table chunk would repeat the table.
			cur = end
			continue
		}
		cur = overlapStart(source, blocks, cur, end, overlap)
	}
	return ranges
}

// overlapStart picks where the chunk after [cur, end) begins: the sentence
// or whitespace boundary nearest the 15% overlap point, so ordinary prose
// chunks overlap. A sentence end within 64 bytes of the point beats a
// nearer plain space; farther away distance decides. A boundary inside an
// atomic block (a table, a fence or a list item that fits) moves to that
// block's start when it is near the overlap point, and otherwise drops the
// overlap: atomic blocks stay whole.
func overlapStart(source string, blocks []block, cur, end, overlap int) int {
	want := end - overlap
	best, bestDist := -1, 0
	sent, sentDist := -1, 0
	backticks := 0
	for i := cur + 1; i < end; i++ {
		if !utf8.RuneStart(source[i]) {
			continue
		}
		r, _ := utf8.DecodeLastRuneInString(source[:i])
		class := -1
		switch {
		case (r == '.' || r == '?' || r == '!') && isSpaceByte(source[i]) && backticks%2 == 0:
			class = 1
		case r == ' ' || r == '\n':
			class = 0
		}
		if class >= 0 {
			dist := want - i
			if dist < 0 {
				dist = -dist
			}
			if class == 1 && (sent < 0 || dist < sentDist) {
				sent, sentDist = i, dist
			}
			if best < 0 || dist < bestDist {
				best, bestDist = i, dist
			}
		}
		if r == '`' {
			backticks++
		}
	}
	if sent >= 0 && sentDist <= 64 {
		best = sent
	}
	if best < 0 {
		return end
	}
	if b := blockAt(blocks, best); best > b.start && atomic(b) {
		if b.start > cur && end-b.start <= overlap+overlap/2 {
			return b.start
		}
		return end
	}
	return best
}

// atomic reports whether a block must never be split by a chunk boundary.
func atomic(b block) bool {
	if b.kind == blockTable {
		return true
	}
	if b.kind == blockFence || b.kind == blockItem {
		return b.end-b.start <= MaxTokens*TokenBytes
	}
	return false
}

// buildCuts lists every legal boundary: block starts, then sentence and
// whitespace boundaries inside blocks too large for one chunk. Tables have no
// interior boundaries; they are never split.
func buildCuts(source string, blocks []block) []cut {
	maxBytes := MaxTokens * TokenBytes
	cuts := []cut{{pos: 0, class: 2}}
	for i, b := range blocks {
		if i > 0 {
			cuts = append(cuts, cut{pos: b.start, class: 2})
		}
		if b.end-b.start > maxBytes && b.kind != blockTable {
			cuts = append(cuts, interiorCuts(source, b)...)
		}
	}
	cuts = append(cuts, cut{pos: len(source), class: 2})
	return cuts
}

// pickCut returns the boundary closest to want inside [lo, hi], preferring
// the highest class and, on ties, the earlier position.
func pickCut(cuts []cut, want, lo, hi int) int {
	best := -1
	bestClass, bestDist := -1, 0
	for _, c := range cuts {
		if c.pos < lo || c.pos > hi {
			continue
		}
		dist := want - c.pos
		if dist < 0 {
			dist = -dist
		}
		better := c.class > bestClass
		if c.class == bestClass && best >= 0 {
			better = dist < bestDist || (dist == bestDist && c.pos < best)
		}
		if better || best < 0 {
			bestClass, bestDist, best = c.class, dist, c.pos
		}
	}
	return best
}

func blockAt(blocks []block, pos int) block {
	cur := blocks[0]
	for _, b := range blocks {
		if b.start <= pos {
			cur = b
		} else {
			break
		}
	}
	return cur
}

// coveringBlock returns the single block that spans [lo, hi] with no
// boundary inside it, when one exists.
func coveringBlock(blocks []block, lo, hi int) (block, bool) {
	for _, b := range blocks {
		if b.start < lo && b.end > hi {
			return b, true
		}
	}
	return block{}, false
}

// interiorCuts lists boundaries inside one oversized block. For code the
// preferred class is the line end; otherwise it is the sentence end outside
// inline code.
func interiorCuts(source string, b block) []cut {
	var out []cut
	backticks := 0
	for i := b.start; i < b.end; {
		r, size := utf8.DecodeRuneInString(source[i:])
		next := i + size
		if next >= b.end {
			break
		}
		after := source[next]
		class := -1
		switch {
		case b.kind == blockFence:
			// Code never cuts at a sentence end, only at a line end.
			switch r {
			case '\n':
				class = 1
			case ' ':
				class = 0
			}
		case r == '.' || r == '?' || r == '!':
			if isSpaceByte(after) && backticks%2 == 0 {
				class = 1
			}
		case r == ' ' || r == '\n':
			class = 0
		}
		if class >= 0 {
			out = append(out, cut{pos: next, class: class})
		}
		if r == '`' {
			backticks++
		}
		i = next
	}
	return out
}

// fallbackSplit cuts inside a block that cannot be kept within the window:
// sentence or line ends first, then whitespace, then the nearest rune
// boundary so the hard maximum always holds. Backtick parity is counted from
// the block start so sentence ends inside inline code are skipped.
func fallbackSplit(source string, from, lo, hi, want int, kind blockKind) int {
	best, bestClass, bestDist := -1, -1, 0
	backticks := strings.Count(source[from:lo], "`")
	for i := lo; i <= hi && i < len(source); i++ {
		if !utf8.RuneStart(source[i]) {
			continue
		}
		r, _ := utf8.DecodeLastRuneInString(source[:i])
		class := -1
		switch {
		case kind == blockFence:
			// Code never cuts at a sentence end, only at a line end.
			switch r {
			case '\n':
				class = 1
			case ' ':
				class = 0
			}
		case (r == '.' || r == '?' || r == '!') && isSpaceByte(source[i]) && backticks%2 == 0:
			class = 1
		case r == ' ' || r == '\n':
			class = 0
		}
		if class >= 0 {
			dist := want - i
			if dist < 0 {
				dist = -dist
			}
			if class > bestClass || (class == bestClass && (best < 0 || dist < bestDist)) {
				bestClass, bestDist, best = class, dist, i
			}
		}
		if r == '`' {
			backticks++
		}
	}
	if best >= 0 {
		return best
	}
	if want < lo {
		want = lo
	}
	return align(source, want, hi)
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\n' || b == '\t' || b == '\r'
}

// align moves pos to a rune boundary: forward to the first start at or
// after it and before hi, or back to the last start at or before hi when
// the window holds none, so a hard cut never splits a rune.
func align(s string, i, hi int) int {
	if i < 0 {
		i = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	if i > hi {
		i = hi
	}
	for i < hi && !utf8.RuneStart(s[i]) {
		i++
	}
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// parseBlocks scans the source once by line and returns its Markdown blocks.
func parseBlocks(source string) []block {
	lines := lineStarts(source)
	var blocks []block
	i := 0
	for i < len(lines) {
		start := lines[i]
		if start >= len(source) {
			break
		}
		if blankLine(source, start) {
			i++
			continue
		}
		var b block
		if end, next := scanFence(source, lines, i); end > start {
			b = block{end: end, kind: blockFence}
			i = next
		} else if title, level, end, next := scanHeading(source, lines, i); level > 0 {
			b = block{end: end, kind: blockHeading, level: level, title: title}
			i = next
		} else if end, next := scanTable(source, lines, i); end > start {
			b = block{end: end, kind: blockTable}
			i = next
		} else if end, next := scanItem(source, lines, i); end > start {
			b = block{end: end, kind: blockItem}
			i = next
		} else {
			end, next := scanParagraph(source, lines, i)
			if end <= start {
				end, next = lineEnd(source, start), i+1
			}
			b = block{end: end, kind: blockParagraph}
			i = next
		}
		for b.end < len(source) && blankLine(source, b.end) {
			b.end = lineEnd(source, b.end)
		}
		if len(blocks) > 0 {
			b.start = blocks[len(blocks)-1].end
		}
		blocks = append(blocks, b)
	}
	if len(blocks) > 0 {
		blocks[len(blocks)-1].end = len(source)
	}
	return blocks
}

type pathMarker struct {
	offset int
	path   string
}

// headingMarkers records the heading path active from each heading line on.
func headingMarkers(blocks []block) []pathMarker {
	var markers []pathMarker
	var stack []string
	for _, b := range blocks {
		if b.kind != blockHeading {
			continue
		}
		for len(stack) >= b.level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, b.title)
		markers = append(markers, pathMarker{offset: b.start, path: strings.Join(stack, " > ")})
	}
	return markers
}

func headingPathAt(offset int, markers []pathMarker) string {
	path := ""
	for _, m := range markers {
		if m.offset > offset {
			break
		}
		path = m.path
	}
	return path
}

func firstNonblank(s string, start, end int) int {
	for i := start; i < end; {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			return i
		}
		i += size
	}
	return start
}

func lineStarts(s string) []int {
	starts := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && i+1 < len(s) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func lineEnd(s string, start int) int {
	for i := start; i < len(s); i++ {
		if s[i] == '\n' {
			return i + 1
		}
	}
	return len(s)
}

func blankLine(s string, start int) bool {
	end := lineEnd(s, start)
	return strings.TrimSpace(s[start:end]) == ""
}

// scanFence returns the byte range of a fenced code block. An unclosed fence
// runs to the end of the source. Syntax inside the fence is never parsed.
func scanFence(source string, lines []int, i int) (end, next int) {
	start := lines[i]
	line := lineText(source, start)
	_, fence := fenceOpen(line)
	if fence == "" {
		return 0, i
	}
	j := i + 1
	for j < len(lines) {
		cur := lineText(source, lines[j])
		if leadingSpaces(cur) <= 3 && strings.HasPrefix(strings.TrimLeft(cur, " "), fence) &&
			strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(cur, " "), fence)) == "" {
			return lineEnd(source, lines[j]), j + 1
		}
		j++
	}
	return len(source), len(lines)
}

// fenceOpen reports an opening fence of at least three backticks or tildes
// indented by at most three spaces.
func fenceOpen(line string) (indent int, fence string) {
	spaces := leadingSpaces(line)
	if spaces > 3 {
		return 0, ""
	}
	rest := strings.TrimSpace(line)
	if len(rest) < 3 {
		return 0, ""
	}
	ch := rest[0]
	if ch != '`' && ch != '~' {
		return 0, ""
	}
	n := 0
	for n < len(rest) && rest[n] == ch {
		n++
	}
	if n < 3 {
		return 0, ""
	}
	return spaces, rest[:n]
}

// scanHeading reports an ATX heading: one to six # characters, up to three
// leading spaces and whitespace after the marker.
func scanHeading(source string, lines []int, i int) (title string, level int, end, next int) {
	start := lines[i]
	line := lineText(source, start)
	spaces := leadingSpaces(line)
	trim := strings.TrimSpace(line)
	if spaces > 3 || len(trim) == 0 || trim[0] != '#' {
		return "", 0, 0, i
	}
	n := 0
	for n < len(trim) && trim[n] == '#' {
		n++
	}
	if n < 1 || n > 6 || n >= len(trim) || !unicode.IsSpace(rune(trim[n])) {
		return "", 0, 0, i
	}
	title = strings.TrimSpace(trim[n+1:])
	if title == "" {
		return "", 0, 0, i
	}
	return title, n, lineEnd(source, start), i + 1
}

// scanTable returns a header row, a GFM delimiter row and the contiguous
// pipe rows after them as one block. A header with only a delimiter row is
// still a table, and a table is atomic at any size.
func scanTable(source string, lines []int, i int) (end, next int) {
	if i+1 >= len(lines) {
		return 0, i
	}
	h1 := lineText(source, lines[i])
	h2 := lineText(source, lines[i+1])
	if !looksTableRow(h1) || !tableDelimiter(h2) {
		return 0, i
	}
	j := i + 2
	for j < len(lines) {
		line := lineText(source, lines[j])
		if strings.TrimSpace(line) == "" || !looksTableRow(line) {
			break
		}
		j++
	}
	return lineEnd(source, lines[j-1]), j
}

func looksTableRow(line string) bool {
	return strings.Contains(strings.TrimSpace(line), "|")
}

// tableDelimiter accepts a GFM delimiter row: every cell is three or more
// dashes with optional outer colons. Escaped pipes and pipes inside inline
// code spans stay part of their cell and do not split it.
func tableDelimiter(line string) bool {
	trim := strings.TrimSpace(line)
	if !strings.Contains(trim, "|") {
		return false
	}
	cells := 0
	for _, p := range strings.Split(trim, "|") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		dashes := 0
		for _, r := range p {
			switch r {
			case '-':
				dashes++
			case ':':
			default:
				return false
			}
		}
		if dashes < 3 {
			return false
		}
		cells++
	}
	return cells > 0
}

// scanItem returns one list item: the marker line plus its indented
// continuation lines. A list splits between items, never inside one that
// fits.
func scanItem(source string, lines []int, i int) (end, next int) {
	start := lines[i]
	if !listMarker(lineText(source, start)) {
		return 0, i
	}
	j := i + 1
	for j < len(lines) {
		line := lineText(source, lines[j])
		if strings.TrimSpace(line) == "" || listMarker(line) {
			break
		}
		if leadingSpaces(line) < 2 {
			break
		}
		j++
	}
	return lineEnd(source, lines[j-1]), j
}

func listMarker(line string) bool {
	trim := strings.TrimLeft(line, " ")
	if leadingSpaces(line) > 3 {
		return false
	}
	if len(trim) >= 2 && (trim[0] == '-' || trim[0] == '+' || trim[0] == '*') && unicode.IsSpace(rune(trim[1])) {
		return true
	}
	i := 0
	for i < len(trim) && trim[i] >= '0' && trim[i] <= '9' {
		i++
	}
	return i > 0 && i < len(trim) && (trim[i] == '.' || trim[i] == ')') &&
		i+1 < len(trim) && unicode.IsSpace(rune(trim[i+1]))
}

// scanParagraph returns a contiguous run of ordinary lines, stopping at
// blank lines and at the start of any other construct.
func scanParagraph(source string, lines []int, i int) (end, next int) {
	j := i + 1
	for j < len(lines) {
		start := lines[j]
		if blankLine(source, start) {
			break
		}
		line := lineText(source, start)
		if _, fence := fenceOpen(line); fence != "" {
			break
		}
		if _, level, _, _ := scanHeading(source, lines, j); level > 0 {
			break
		}
		if looksTableRow(line) && j+1 < len(lines) && tableDelimiter(lineText(source, lines[j+1])) {
			break
		}
		if listMarker(line) {
			break
		}
		j++
	}
	return lineEnd(source, lines[j-1]), j
}

func lineText(source string, start int) string {
	end := lineEnd(source, start)
	if end > 0 && source[end-1] == '\n' {
		end--
	}
	return source[start:end]
}

func leadingSpaces(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}
