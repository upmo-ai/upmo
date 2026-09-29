package domain

import (
	"errors"
	"testing"
)

func TestPlaceEditorPlacesService(t *testing.T) {
	cases := []struct {
		platform, status string
		want             Service
	}{
		{" Compose ", " Working ", Service{Name: EditorService, Volume: ScratchName, Mode: ModeReadOnly, Beside: BesideService, Platform: PlatformCompose}},
		{"Kubernetes", "Checking", Service{Name: EditorService, Volume: ScratchName, Mode: ModeReadWrite, Beside: BesidePod, Platform: PlatformKubernetes}},
		{"Kubernetes", "Done", Service{Name: EditorService, Volume: ScratchName, Mode: ModeReadWrite, Beside: BesidePod, Platform: PlatformKubernetes}},
		{"Kubernetes", "Failed", Service{Name: EditorService, Volume: ScratchName, Mode: ModeReadWrite, Beside: BesidePod, Platform: PlatformKubernetes}},
	}
	for _, c := range cases {
		got, err := PlaceEditor(c.platform, c.status)
		if err != nil {
			t.Fatalf("PlaceEditor(%q, %q) error = %v", c.platform, c.status, err)
		}
		if got != c.want {
			t.Fatalf("PlaceEditor(%q, %q) = %+v, want %+v", c.platform, c.status, got, c.want)
		}
	}
}

func TestPlaceEditorDiffersOnlyBeside(t *testing.T) {
	compose, err := PlaceEditor(PlatformCompose, "Working")
	if err != nil {
		t.Fatalf("PlaceEditor(%q, Working) error = %v", PlatformCompose, err)
	}
	kubernetes, err := PlaceEditor(PlatformKubernetes, "Working")
	if err != nil {
		t.Fatalf("PlaceEditor(%q, Working) error = %v", PlatformKubernetes, err)
	}
	if compose.Name != kubernetes.Name || compose.Volume != kubernetes.Volume || compose.Mode != kubernetes.Mode {
		t.Fatalf("PlaceEditor Working: compose = %+v, kubernetes = %+v, want same Name, Volume, and Mode", compose, kubernetes)
	}
	if compose.Beside == kubernetes.Beside {
		t.Fatalf("PlaceEditor Working: Beside = %q on both, want them to differ", compose.Beside)
	}
}

func TestPlaceEditorRefuses(t *testing.T) {
	cases := []struct{ platform, status string }{
		{"compose", "Preparing"},
		{"kubernetes", "Planning"},
		{"compose", ""},
		{"kubernetes", "working"},
		{"", "Working"},
		{"pod", "Working"},
		{"swarm", "Working"},
	}
	for _, c := range cases {
		got, err := PlaceEditor(c.platform, c.status)
		if !errors.Is(err, ErrEditor) {
			t.Fatalf("PlaceEditor(%q, %q) error = %v, want %v", c.platform, c.status, err, ErrEditor)
		}
		if got != (Service{}) {
			t.Fatalf("PlaceEditor(%q, %q) = %+v, want zero Service", c.platform, c.status, got)
		}
	}
}
