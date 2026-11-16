package domain

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testPack() Pack {
	return Pack{
		Schema:      PackSchemaV1,
		ID:          "upmo/purchase-invoices@1.0",
		Version:     "1.0.0",
		Name:        "Purchase Invoices",
		Description: "Books supplier invoices.",
		Agent: PackAgent{
			Name:                 "Purchase Invoices",
			Slug:                 "purchase-invoices",
			Role:                 "Accounts Payable",
			Instructions:         "Check VAT and PO numbers before posting.",
			DefaultRoute:         "work",
			DefaultModelProvider: "Anthropic",
			DefaultModelID:       "claude-sonnet-4-5",
			ToolClasses:          []string{"connector", "knowledge"},
			ConnectorIDs:         []string{"xero"},
			PlanApproval:         PlanApprovalAlways,
			CheckCriteria:        []json.RawMessage{json.RawMessage(`{"rule":"vat_matches"}`)},
		},
		Lessons: []PackLesson{
			{Title: "EU Reverse Charge", Cue: "EU supplier invoice", Body: "Verify VAT ID.", AlwaysInline: true},
			{Title: "Credit Notes", Cue: "Negative total", Body: "Match original bill.", AlwaysInline: false},
		},
		Templates: []PackTemplate{
			{Name: "Book invoice", Goal: "Record incoming supplier invoice", Flow: "extract -> verify -> post", CheckCriteria: []json.RawMessage{json.RawMessage(`"balanced"`)}},
		},
		ConnectorRules: []PackConnectorRule{
			{Connector: "xero", ResourceScope: "invoices", Changes: "ask"},
		},
		Schedules: []PackSchedule{
			{Name: "Daily check", Cron: "0 9 * * 1-5", Timezone: "Europe/Berlin"},
		},
	}
}

func TestPackSignAndVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := SignPack(testPack(), "test-key-1", priv)
	if err != nil {
		t.Fatal(err)
	}
	keys := TrustKeys{"test-key-1": pub}

	got, err := VerifyPack(raw, keys)
	if err != nil {
		t.Fatalf("VerifyPack = %v", err)
	}
	if got.ID != "upmo/purchase-invoices@1.0" || got.Agent.Slug != "purchase-invoices" || len(got.Lessons) != 2 {
		t.Fatalf("decoded pack = %+v", got)
	}

	// Tampering any manifest field invalidates the signature.
	tampered := strings.Replace(string(raw), `"changes":"ask"`, `"changes":"allow"`, 1)
	if _, err := VerifyPack([]byte(tampered), keys); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tampered manifest = %v, want ErrInvalidSignature", err)
	}

	// Untrusted / wrong key_id fails.
	if _, err := VerifyPack(raw, DefaultPackKeys()); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("wrong key = %v, want ErrUntrustedKey", err)
	}

	// Valid signature under another trusted key with wrong public key fails.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPack(raw, TrustKeys{"test-key-1": otherPub}); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("mismatched pubkey = %v, want ErrInvalidSignature", err)
	}
}

func TestPackRejectsMalformedSchema(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := TrustKeys{"test-key-1": pub}

	cases := map[string]func(*Pack){
		"wrong schema":        func(p *Pack) { p.Schema = "upmo.pack/v2" },
		"missing id":          func(p *Pack) { p.ID = "" },
		"bad connector_rules": func(p *Pack) { p.ConnectorRules[0].Changes = "yolo" },
		"missing slug":        func(p *Pack) { p.Agent.Slug = "" },
	}
	for name, mutate := range cases {
		p := testPack()
		p.KeyID = "test-key-1"
		mutate(&p)
		// Sign raw JSON directly even if Validate() would fail, to verify VerifyPack rejects it.
		b, _ := json.Marshal(p)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		delete(m, "ed25519_sig")
		payloadBytes, _ := json.Marshal(m)
		canon, _ := CanonicalJSON(payloadBytes)
		sig := ed25519.Sign(priv, canon)
		m["ed25519_sig"] = base64.StdEncoding.EncodeToString(sig)
		signed, _ := json.Marshal(m)
		if _, err := VerifyPack(signed, keys); !errors.Is(err, ErrMalformedPack) {
			t.Fatalf("%s: got %v, want ErrMalformedPack", name, err)
		}
	}
}

func TestParseTrustKeysAndCanonicalJSON(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(pub)
	keys, err := ParseTrustKeys("custom-k1="+b64, DefaultPackKeys())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys[DefaultMarketplaceKeyID]; !ok {
		t.Fatal("missing default marketplace key")
	}
	if _, ok := keys["custom-k1"]; !ok {
		t.Fatal("missing custom key")
	}
	if _, err := ParseTrustKeys("bad-entry", nil); err == nil {
		t.Fatal("malformed trust key entry succeeded")
	}

	canon, err := CanonicalJSON([]byte(`{ "b": [1.0, -0.0, true], "a": "<hi>&" }`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":"<hi>&","b":[1,0,true]}`; string(canon) != want {
		t.Fatalf("canon = %s, want %s", canon, want)
	}
}

func TestCanonicalJSONRFC8785(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "control character escaping",
			in:   `{"s":"\u0000\u0001\u0007\b\t\n\u000b\f\r\u001f\"\\"}`,
			want: `{"s":"\u0000\u0001\u0007\b\t\n\u000b\f\r\u001f\"\\"}`,
		},
		{
			name: "utf16 code-unit key ordering BMP vs non-BMP surrogates",
			// In UTF-16 code units:
			// "\r" (0x000D) < "1" (0x0031) < "\u0080" (0x0080) < "\u00f6" (0x00F6)
			// < "\u20ac" (0x20AC) < "\ud7ff" (0xD7FF) < "\ud800\udc00" (0xD800,0xDC00)
			// < "\ud83d\ude00" (0xD83D,0xDE00) < "\ue000" (0xE000) < "\ufb33" (0xFB33)
			// Note non-BMP surrogates (0xD800..0xDFFF) sort BEFORE BMP U+E000..U+FFFF.
			in:   `{"\ufb33":1,"\ue000":2,"\ud83d\ude00":3,"\ud800\udc00":4,"\ud7ff":5,"\u20ac":6,"\u00f6":7,"\u0080":8,"1":9,"\r":10}`,
			want: `{"\r":10,"1":9,"` + "\u0080" + `":8,"ö":7,"€":6,"퟿":5,"𐀀":4,"😀":3,"":2,"דּ":1}`,
		},
		{
			name: "utf16 prefix key ordering",
			in:   `{"ab":1,"a":2,"":3}`,
			want: `{"":3,"a":2,"ab":1}`,
		},
		{
			name: "es6 numbers and exponent form",
			in:   `[0, -0.0, 1.0, 1e+00, 1e5, 1e-6, 1e-7, -1.5e-7, 1e20, 1e21, 1.23e+25, 9.99999999999999e-7]`,
			want: `[0,0,1,1,100000,0.000001,1e-7,-1.5e-7,100000000000000000000,1e+21,1.23e+25,9.99999999999999e-7]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalJSON([]byte(tc.in))
			if err != nil {
				t.Fatalf("CanonicalJSON(%s) error = %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Fatalf("CanonicalJSON = %s, want %s", got, tc.want)
			}
		})
	}

	// Direct compareUTF16 checks for non-BMP surrogate vs BMP and equal strings.
	if cmp := compareUTF16("\U0001F600", "\uE000"); cmp >= 0 {
		t.Fatalf("compareUTF16(U+1F600, U+E000) = %d, want < 0", cmp)
	}
	if cmp := compareUTF16("\uD7FF", "\U00010000"); cmp >= 0 {
		t.Fatalf("compareUTF16(U+D7FF, U+10000) = %d, want < 0", cmp)
	}
	if cmp := compareUTF16("same", "same"); cmp != 0 {
		t.Fatalf("compareUTF16(same, same) = %d, want 0", cmp)
	}
}
