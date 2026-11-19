package domain

import (
	"testing"
	"time"
)

func TestNextModelMissState(t *testing.T) {
	cases := []struct {
		name        string
		misses      int
		present     bool
		wantMisses  int
		wantRetired bool
	}{
		{name: "first miss keeps model active", misses: 0, present: false, wantMisses: 1, wantRetired: false},
		{name: "second consecutive miss retires model", misses: 1, present: false, wantMisses: 2, wantRetired: true},
		{name: "subsequent miss stays retired", misses: 2, present: false, wantMisses: 3, wantRetired: true},
		{name: "present after one miss resets", misses: 1, present: true, wantMisses: 0, wantRetired: false},
		{name: "reappearing retired model clears retirement", misses: 2, present: true, wantMisses: 0, wantRetired: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMisses, gotRetired := NextModelMissState(tc.misses, tc.present)
			if gotMisses != tc.wantMisses || gotRetired != tc.wantRetired {
				t.Fatalf("NextModelMissState(%d, %v) = (%d, %v), want (%d, %v)",
					tc.misses, tc.present, gotMisses, gotRetired, tc.wantMisses, tc.wantRetired)
			}
		})
	}
}

func TestAccountCheckOffset(t *testing.T) {
	id1 := "11111111-1111-4111-8111-111111111111"
	id2 := "22222222-2222-4222-8222-222222222222"
	off1 := AccountCheckOffset(id1)
	off2 := AccountCheckOffset(id2)
	if off1 < 0 || off1 >= 24*time.Hour {
		t.Fatalf("off1 = %v, want in [0, 24h)", off1)
	}
	if off2 < 0 || off2 >= 24*time.Hour {
		t.Fatalf("off2 = %v, want in [0, 24h)", off2)
	}
	if off1 != AccountCheckOffset(id1) {
		t.Fatal("AccountCheckOffset is not deterministic")
	}
	if off1 == off2 {
		t.Fatalf("expected distinct offsets for %s and %s, got %v", id1, id2, off1)
	}
}
