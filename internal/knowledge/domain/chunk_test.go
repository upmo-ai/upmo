package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkKeepsAShortSourceWhole(t *testing.T) {
	got, err := Chunk("Ada prefers oolong tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "Ada prefers oolong tea" {
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
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is not valid text", i)
		}
		at := strings.Index(source[cursor:], chunk)
		if at < 0 {
			t.Fatalf("chunk %d is not a substring", i)
		}
		abs := cursor + at
		if i < len(got)-1 && (len(chunk) < minBytes || len(chunk) > maxBytes) {
			t.Fatalf("chunk %d length %d", i, len(chunk))
		}
		if i == len(got)-1 && (len(chunk) == 0 || len(chunk) > maxBytes) {
			t.Fatalf("tail length %d", len(chunk))
		}
		if i > 0 {
			overlap := prevEnd - abs
			if overlap <= 0 {
				t.Fatalf("chunk %d does not overlap the previous one", i)
			}
			want := TargetTokens * TokenBytes * OverlapPercent / 100
			if i == 1 && math.Abs(float64(overlap-want)) > 40 {
				t.Fatalf("overlap = %d, want about %d", overlap, want)
			}
		}
		prevEnd = abs + len(chunk)
		cursor = abs + 1
	}
}

func TestChunkDoesNotSplitARune(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&b, "caf\u00e9%04d ", i)
	}
	source := b.String()
	got, err := Chunk(source)
	if err != nil {
		t.Fatal(err)
	}
	for i, chunk := range got {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d split a rune", i)
		}
		if !strings.Contains(source, chunk) {
			t.Fatalf("chunk %d is not verbatim", i)
		}
	}
}

func numberedSource(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "w%04d ", i)
	}
	return b.String()
}
