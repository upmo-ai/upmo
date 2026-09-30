package domain

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestOutlineEmptyReturnsEmptySlice(t *testing.T) {
	for _, in := range [][]string{nil, {}} {
		got, err := Outline(in)
		if err != nil {
			t.Fatalf("Outline(%q) error = %v, want nil", in, err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("Outline(%q) = %#v, want non-nil empty slice", in, got)
		}
	}
}

func TestOutlineTrimsAndKeepsOrder(t *testing.T) {
	got, err := Outline([]string{"  Intro ", "", "   ", "Usage", "Intro"})
	if err != nil {
		t.Fatalf("Outline() error = %v, want nil", err)
	}
	want := []string{"Intro", "Usage", "Intro"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Outline() = %q, want %q", got, want)
	}
}

func TestOutlineTooLongHeadingRefusesWholeList(t *testing.T) {
	got, err := Outline([]string{"Intro", strings.Repeat("é", 201), "Usage"})
	if !errors.Is(err, ErrFile) {
		t.Fatalf("Outline() error = %v, want %v", err, ErrFile)
	}
	if got != nil {
		t.Fatalf("Outline() = %q, want nil", got)
	}
}

func TestOutlineAllowsHeadingOfTwoHundredRunes(t *testing.T) {
	heading := strings.Repeat("é", 200)
	got, err := Outline([]string{heading})
	if err != nil {
		t.Fatalf("Outline() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != heading {
		t.Fatalf("Outline() = %q, want one heading of 200 runes", got)
	}
}

func TestOutlineRefusesHeadingWithNewline(t *testing.T) {
	got, err := Outline([]string{"Intro", "two\nlines"})
	if !errors.Is(err, ErrFile) {
		t.Fatalf("Outline() error = %v, want %v", err, ErrFile)
	}
	if got != nil {
		t.Fatalf("Outline() = %q, want nil", got)
	}
}

func TestOutlineRefusesMoreThanCap(t *testing.T) {
	headings := make([]string, 0, OutlineCap+3)
	for i := 0; i < OutlineCap+1; i++ {
		headings = append(headings, fmt.Sprintf("Heading %d", i))
	}
	headings = append(headings, "", "  ")

	got, err := Outline(headings)
	if !errors.Is(err, ErrFile) {
		t.Fatalf("Outline(%d kept) error = %v, want %v", OutlineCap+1, err, ErrFile)
	}
	if got != nil {
		t.Fatalf("Outline(%d kept) = %q, want nil", OutlineCap+1, got)
	}
}

func TestOutlineAllowsCapWithBlanks(t *testing.T) {
	headings := make([]string, 0, OutlineCap*2)
	for i := 0; i < OutlineCap; i++ {
		headings = append(headings, fmt.Sprintf("Heading %d", i), " ")
	}

	got, err := Outline(headings)
	if err != nil {
		t.Fatalf("Outline() error = %v, want nil", err)
	}
	if len(got) != OutlineCap {
		t.Fatalf("len(Outline()) = %d, want %d", len(got), OutlineCap)
	}
}

func TestAdmitRangeAllowsBudget(t *testing.T) {
	for _, r := range [][2]int{{0, 1}, {0, ReadBudget}, {100, 100 + ReadBudget}} {
		if err := AdmitRange(r[0], r[1]); err != nil {
			t.Fatalf("AdmitRange(%d, %d) error = %v, want nil", r[0], r[1], err)
		}
	}
}

func TestAdmitRangeRefusesOutsideBudget(t *testing.T) {
	for _, r := range [][2]int{
		{0, ReadBudget + 1},
		{10, 10 + ReadBudget + 1},
		{0, 0},
		{42, 42},
		{10, 5},
		{-1, 10},
	} {
		if err := AdmitRange(r[0], r[1]); !errors.Is(err, ErrFile) {
			t.Fatalf("AdmitRange(%d, %d) error = %v, want %v", r[0], r[1], err, ErrFile)
		}
	}
}

func TestSeenMatchesNormalisedHash(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	known := []string{strings.Repeat("0", 64), "  " + strings.ToUpper(hash) + "\n"}

	got, err := Seen(" "+strings.ToUpper(hash)+" ", known)
	if err != nil {
		t.Fatalf("Seen() error = %v, want nil", err)
	}
	if !got {
		t.Fatal("Seen() = false, want true")
	}
}

func TestSeenReportsUnknownHash(t *testing.T) {
	for _, known := range [][]string{nil, {strings.Repeat("0", 64)}} {
		got, err := Seen(strings.Repeat("f", 64), known)
		if err != nil {
			t.Fatalf("Seen(%q) error = %v, want nil", known, err)
		}
		if got {
			t.Fatalf("Seen(%q) = true, want false", known)
		}
	}
}

func TestSeenRefusesBadHash(t *testing.T) {
	for _, hash := range []string{
		"",
		"   ",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("g", 64),
		strings.Repeat("a", 62) + "é",
	} {
		got, err := Seen(hash, []string{hash})
		if !errors.Is(err, ErrFile) {
			t.Fatalf("Seen(%q) error = %v, want %v", hash, err, ErrFile)
		}
		if got {
			t.Fatalf("Seen(%q) = true, want false", hash)
		}
	}
}

func TestSeenSkipsBadKnownEntry(t *testing.T) {
	hash := strings.Repeat("c", 64)
	known := []string{"", "not-a-hash", strings.Repeat("c", 63), strings.Repeat("z", 64), hash}

	got, err := Seen(hash, known)
	if err != nil {
		t.Fatalf("Seen() error = %v, want nil", err)
	}
	if !got {
		t.Fatal("Seen() = false, want true")
	}

	got, err = Seen(hash, known[:4])
	if err != nil {
		t.Fatalf("Seen() error = %v, want nil", err)
	}
	if got {
		t.Fatal("Seen() = true, want false")
	}
}
