package writeguard

import "testing"

func TestWriteCombinations(t *testing.T) {
	cases := []struct {
		untrusted, private, external bool
		want                         Verdict
	}{
		{false, false, false, Allow},
		{true, false, false, Allow},
		{false, true, false, Allow},
		{false, false, true, Allow},
		{true, true, false, Allow},
		{true, false, true, Allow},
		{false, true, true, Allow},
		{true, true, true, Ask},
	}
	for _, tc := range cases {
		x := Exposure{Untrusted: tc.untrusted, Private: tc.private, External: tc.external}
		if got := Write(x); got != tc.want {
			t.Fatalf("%v = %s, want %s", x, got, tc.want)
		}
		if Armed(x) != (tc.want == Ask) {
			t.Fatalf("Armed(%v) = %v", x, Armed(x))
		}
	}
}

func TestTightenNeverLoosens(t *testing.T) {
	armed := Exposure{Untrusted: true, Private: true, External: true}
	clear := Exposure{}
	if got := Tighten("block", armed); got != "block" {
		t.Fatalf("block armed = %s", got)
	}
	if got := Tighten("block", clear); got != "block" {
		t.Fatalf("block clear = %s", got)
	}
	if got := Tighten("ask", armed); got != "ask" {
		t.Fatalf("ask armed = %s", got)
	}
	if got := Tighten("ask", clear); got != "ask" {
		t.Fatalf("ask clear = %s", got)
	}
	if got := Tighten("allow", armed); got != "ask" {
		t.Fatalf("allow armed = %s", got)
	}
	if got := Tighten("allow", clear); got != "allow" {
		t.Fatalf("allow clear = %s", got)
	}
}

func FuzzWrite(f *testing.F) {
	f.Add(true, true, true)
	f.Add(false, false, false)
	f.Add(true, false, true)
	f.Fuzz(func(t *testing.T, untrusted, private, external bool) {
		x := Exposure{Untrusted: untrusted, Private: private, External: external}
		got := Write(x)
		if untrusted && private && external {
			if got != Ask {
				t.Fatalf("armed write = %s", got)
			}
		} else if got != Allow {
			t.Fatalf("clear write = %s", got)
		}
		tight := Tighten("allow", x)
		if Armed(x) && tight != "ask" {
			t.Fatalf("tighten allow armed = %s", tight)
		}
		if Tighten("block", x) != "block" {
			t.Fatal("block loosened")
		}
		if Tighten("ask", x) != "ask" {
			t.Fatal("ask loosened")
		}
	})
}
