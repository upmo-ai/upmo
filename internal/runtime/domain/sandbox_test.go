package domain

import (
	"errors"
	"reflect"
	"testing"
)

func TestNeedsSandboxWithoutRepositoryOrClasses(t *testing.T) {
	got, err := NeedsSandbox(" ", nil)
	if err != nil {
		t.Fatalf("NeedsSandbox() error = %v", err)
	}
	if got {
		t.Fatal("NeedsSandbox() = true, want false")
	}
}

func TestNeedsSandboxWithRepository(t *testing.T) {
	got, err := NeedsSandbox("  org/name  ", nil)
	if err != nil {
		t.Fatalf("NeedsSandbox() error = %v", err)
	}
	if !got {
		t.Fatal("NeedsSandbox() = false, want true")
	}
}

func TestNeedsSandboxWithNonSandboxClasses(t *testing.T) {
	got, err := NeedsSandbox("", []Class{ClassConnector, ClassKnowledge, ClassWeb})
	if err != nil {
		t.Fatalf("NeedsSandbox() error = %v", err)
	}
	if got {
		t.Fatal("NeedsSandbox() = true, want false")
	}
}

func TestNeedsSandboxWithShell(t *testing.T) {
	got, err := NeedsSandbox("", []Class{ClassShell})
	if err != nil {
		t.Fatalf("NeedsSandbox() error = %v", err)
	}
	if !got {
		t.Fatal("NeedsSandbox() = false, want true")
	}
}

func TestNeedsSandboxWithFile(t *testing.T) {
	got, err := NeedsSandbox("", []Class{ClassFile})
	if err != nil {
		t.Fatalf("NeedsSandbox() error = %v", err)
	}
	if !got {
		t.Fatal("NeedsSandbox() = false, want true")
	}
}

func TestNeedsSandboxWithShellAndConnector(t *testing.T) {
	got, err := NeedsSandbox("", []Class{ClassShell, ClassConnector})
	if err != nil {
		t.Fatalf("NeedsSandbox() error = %v", err)
	}
	if !got {
		t.Fatal("NeedsSandbox() = false, want true")
	}
}

func TestNeedsSandboxRefusesUnknownClass(t *testing.T) {
	got, err := NeedsSandbox("org/name", []Class{ClassShell, "memory"})
	if !errors.Is(err, ErrClass) {
		t.Fatalf("NeedsSandbox() error = %v, want %v", err, ErrClass)
	}
	if got {
		t.Fatal("NeedsSandbox() = true, want false")
	}
}

func TestNeedsSandboxRefusesBlankClass(t *testing.T) {
	got, err := NeedsSandbox("", []Class{"   "})
	if !errors.Is(err, ErrClass) {
		t.Fatalf("NeedsSandbox() error = %v, want %v", err, ErrClass)
	}
	if got {
		t.Fatal("NeedsSandbox() = true, want false")
	}
}

func TestNeedsSandboxDoesNotMutateClasses(t *testing.T) {
	classes := []Class{" shell ", ClassConnector}
	want := append([]Class(nil), classes...)

	_, err := NeedsSandbox("", classes)
	if err != nil {
		t.Fatalf("NeedsSandbox() error = %v", err)
	}
	if !reflect.DeepEqual(classes, want) {
		t.Fatalf("NeedsSandbox() classes = %v, want %v", classes, want)
	}
}
