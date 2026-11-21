package domain

import (
	"errors"
	"testing"
	"time"
)

func TestFit(t *testing.T) {
	if err := Fit(nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := Fit([]string{" tools "}, []string{"TOOLS", "vision"}); err != nil {
		t.Fatal(err)
	}
	if err := Fit([]string{"vision"}, []string{"tools"}); !errors.Is(err, ErrCapability) {
		t.Fatalf("err = %v", err)
	}
	if err := Fit([]string{"context"}, []string{"context"}); !errors.Is(err, ErrCapability) {
		t.Fatalf("unknown cap err = %v", err)
	}
}

func TestGateCooldown(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var g Gate
	g = g.Note(now, ClassRateLimit, 5*time.Second)
	if g.Closed(now) {
		t.Fatal("retry-after should cool at once")
	}
	if !g.Closed(now.Add(5 * time.Second)) {
		t.Fatal("cooldown should end at the retry-after")
	}
	g = Gate{}
	g = g.Note(now, ClassTransient, 0)
	g = g.Note(now, ClassTransient, 0)
	if !g.Closed(now) {
		t.Fatal("two failures stay closed")
	}
	g = g.Note(now, ClassTransient, 0)
	if g.Closed(now) || g.Closed(now.Add(DefaultCool-time.Nanosecond)) {
		t.Fatal("third failure should open the default cooldown")
	}
	if !g.Closed(now.Add(DefaultCool)) {
		t.Fatal("default cooldown should end")
	}
	g = g.Note(now.Add(DefaultCool), ClassOK, 0)
	if g.Failures != 0 || !g.Until.IsZero() {
		t.Fatalf("success = %+v", g)
	}
	open := Gate{Until: now.Add(time.Minute), Failures: 2}
	if kept := open.Note(now, ClassCaller, time.Minute); kept != open {
		t.Fatal("a caller fault must leave the gate")
	}
	bad := Gate{}.Note(now, ClassCredential, 0)
	if bad.Closed(now) {
		t.Fatal("a credential failure cools the key")
	}
	if NextAction(ClassModelAccess, 0) != ActionNext {
		t.Fatal("model-access failure should advance to the next link")
	}
	modelAccess := Gate{}.Note(now, ClassModelAccess, 0)
	if modelAccess.Closed(now.Add(DefaultCool - time.Nanosecond)) {
		t.Fatal("model-access failure should cool the key for DefaultCool")
	}
}
