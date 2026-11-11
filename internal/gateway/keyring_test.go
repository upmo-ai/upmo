package gateway

import (
	"encoding/base64"
	"strings"
	"testing"
)

func b64(b byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(b), 32)))
}

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestKeyringFromEnv(t *testing.T) {
	t.Run("unset is no ring and no error", func(t *testing.T) {
		k, err := KeyringFromEnv(lookup(nil), nil)
		if err != nil || k.Ready() {
			t.Fatalf("ring ready %v, err %v", k.Ready(), err)
		}
		k, err = KeyringFromEnv(lookup(map[string]string{EnvMasterKey: "  "}), nil)
		if err != nil || k.Ready() {
			t.Fatalf("blank: ring ready %v, err %v", k.Ready(), err)
		}
	})
	t.Run("current key under the default id", func(t *testing.T) {
		k, err := KeyringFromEnv(lookup(map[string]string{EnvMasterKey: b64('a')}), nil)
		if err != nil || !k.Ready() {
			t.Fatalf("ready %v, err %v", k.Ready(), err)
		}
		sealed, err := k.Seal("r", "x")
		if err != nil || sealed.KeyID != "k1" {
			t.Fatalf("sealed %+v, err %v", sealed, err)
		}
	})
	t.Run("rotation reads old and writes new", func(t *testing.T) {
		old, _ := KeyringFromEnv(lookup(map[string]string{EnvMasterKey: b64('a')}), nil)
		sealedOld, _ := old.Seal("r", "old")
		env := map[string]string{EnvMasterKey: b64('b'), EnvMasterKeyID: "k2"}
		k, err := KeyringFromEnv(lookup(env), []string{
			"PATH=/bin",
			EnvMasterKeyPrevious + "k1=" + b64('a'),
			EnvMasterKeyPrevious + "k0=",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := k.Open("r", sealedOld); err != nil || got != "old" {
			t.Fatalf("old value: %q, %v", got, err)
		}
		if sealed, _ := k.Seal("r", "new"); sealed.KeyID != "k2" {
			t.Fatalf("sealed with %q", sealed.KeyID)
		}
	})
	bad := map[string]struct {
		env     map[string]string
		environ []string
	}{
		"not base64":       {map[string]string{EnvMasterKey: "not base64 !!"}, nil},
		"wrong length":     {map[string]string{EnvMasterKey: base64.StdEncoding.EncodeToString([]byte("short"))}, nil},
		"bad id":           {map[string]string{EnvMasterKey: b64('a'), EnvMasterKeyID: "k 1"}, nil},
		"bad previous":     {map[string]string{EnvMasterKey: b64('a')}, []string{EnvMasterKeyPrevious + "k0=zzz"}},
		"previous repeats": {map[string]string{EnvMasterKey: b64('a')}, []string{EnvMasterKeyPrevious + "k1=" + b64('b')}},
		"bad previous id":  {map[string]string{EnvMasterKey: b64('a')}, []string{EnvMasterKeyPrevious + "k-0=" + b64('b')}},
	}
	for name, tt := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := KeyringFromEnv(lookup(tt.env), tt.environ)
			if err == nil {
				t.Fatal("accepted")
			}
			for _, v := range tt.env {
				if v != "" && strings.Contains(err.Error(), v) {
					t.Fatalf("error repeats a value: %v", err)
				}
			}
		})
	}
}

func TestEveryAdapterCanBeDispatchedAndProbed(t *testing.T) {
	for name, a := range adapters() {
		if name == "" || a == nil {
			t.Fatalf("adapter %q", name)
		}
	}
	p := NewProviders(nil, Keyring{}, nil)
	for name := range adapters() {
		if !p.Known(name) || p.Probes[name] == nil {
			t.Errorf("adapter %q is not known to the provider settings", name)
		}
	}
	for _, name := range []string{"", "cohere", "openai", "ollama", "FAKE"} {
		if p.Known(name) {
			t.Errorf("adapter %q should not be known", name)
		}
	}
}
