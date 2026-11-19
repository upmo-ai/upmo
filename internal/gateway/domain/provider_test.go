package domain

import (
	"net/netip"
	"strings"
	"testing"
)

func TestCleanBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		plan    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty is empty", PlanMetered, "", "", false},
		{"spaces only is empty", PlanMetered, "   ", "", false},
		{"https metered", PlanMetered, "https://api.example.com/v1", "https://api.example.com/v1", false},
		{"trailing slash goes", PlanMetered, "https://api.example.com/", "https://api.example.com", false},
		{"host is lowered", PlanSubscription, "HTTPS://API.Example.COM", "https://api.example.com", false},
		{"port kept", PlanMetered, "https://api.example.com:8443/x", "https://api.example.com:8443/x", false},
		{"public literal", PlanMetered, "https://8.8.8.8/v1", "https://8.8.8.8/v1", false},
		{"public v6 literal", PlanMetered, "https://[2606:4700:4700::1111]/v1", "https://[2606:4700:4700::1111]/v1", false},

		{"http refused for metered", PlanMetered, "http://api.example.com", "", true},
		{"http refused for subscription", PlanSubscription, "http://api.example.com", "", true},
		{"http allowed for local", PlanLocal, "http://gpu.example.com:11434", "http://gpu.example.com:11434", false},
		{"https allowed for local", PlanLocal, "https://gpu.example.com", "https://gpu.example.com", false},
		{"other scheme", PlanLocal, "ftp://example.com", "", true},
		{"no scheme", PlanMetered, "api.example.com", "", true},
		{"file scheme", PlanLocal, "file:///etc/passwd", "", true},
		{"javascript scheme", PlanLocal, "javascript:alert(1)", "", true},

		{"userinfo user", PlanMetered, "https://user@api.example.com", "", true},
		{"userinfo password", PlanMetered, "https://user:pw@api.example.com", "", true},
		{"userinfo local", PlanLocal, "http://user:pw@127.0.0.1:8080", "", true},
		{"query", PlanMetered, "https://api.example.com/v1?x=1", "", true},
		{"empty query mark", PlanMetered, "https://api.example.com/v1?", "", true},
		{"fragment", PlanMetered, "https://api.example.com/v1#x", "", true},
		{"control char", PlanMetered, "https://api.example.com/\x00", "", true},
		{"newline", PlanMetered, "https://api.example.com/\nx", "", true},
		{"too long", PlanMetered, "https://example.com/" + strings.Repeat("a", 2100), "", true},
		{"port zero", PlanMetered, "https://example.com:0", "", true},
		{"port too big", PlanMetered, "https://example.com:70000", "", true},

		{"loopback metered", PlanMetered, "https://127.0.0.1", "", true},
		{"loopback v6 metered", PlanMetered, "https://[::1]", "", true},
		{"private 10 metered", PlanMetered, "https://10.0.0.5", "", true},
		{"private 172 metered", PlanSubscription, "https://172.16.4.4", "", true},
		{"private 192 metered", PlanMetered, "https://192.168.1.1", "", true},
		{"unique local metered", PlanMetered, "https://[fd12::1]", "", true},
		{"mapped loopback metered", PlanMetered, "https://[::ffff:127.0.0.1]", "", true},
		{"localhost metered", PlanMetered, "https://localhost:8080", "", true},
		{"sub localhost metered", PlanMetered, "https://app.localhost", "", true},
		{"localhost trailing dot metered", PlanMetered, "https://localhost.", "", true},
		{"unspecified metered", PlanMetered, "https://0.0.0.0", "", true},

		{"loopback local", PlanLocal, "http://127.0.0.1:11434", "http://127.0.0.1:11434", false},
		{"loopback v6 local", PlanLocal, "http://[::1]:11434", "http://[::1]:11434", false},
		{"private local", PlanLocal, "http://192.168.1.50:8000", "http://192.168.1.50:8000", false},
		{"localhost local", PlanLocal, "http://localhost:11434", "http://localhost:11434", false},
		{"tailscale local", PlanLocal, "http://100.64.1.2:8000", "http://100.64.1.2:8000", false},
		{"unspecified local", PlanLocal, "http://0.0.0.0:8000", "", true},

		{"metadata ip metered", PlanMetered, "https://169.254.169.254", "", true},
		{"metadata ip local", PlanLocal, "http://169.254.169.254/latest", "", true},
		{"link-local other local", PlanLocal, "http://169.254.1.1", "", true},
		{"link-local v6", PlanLocal, "http://[fe80::1]", "", true},
		{"link-local v6 zone", PlanLocal, "http://[fe80::1%25eth0]", "", true},
		{"mapped metadata local", PlanLocal, "http://[::ffff:169.254.169.254]", "", true},
		{"nat64 metadata local", PlanLocal, "http://[64:ff9b::a9fe:a9fe]", "", true},
		{"aws v6 metadata local", PlanLocal, "http://[fd00:ec2::254]", "", true},
		{"alibaba metadata local", PlanLocal, "http://100.100.100.200", "", true},
		{"gcp metadata name", PlanMetered, "https://metadata.google.internal", "", true},
		{"gcp metadata name local", PlanLocal, "http://metadata.google.internal/computeMetadata", "", true},
		{"gcp metadata name upper", PlanLocal, "http://METADATA.GOOGLE.INTERNAL", "", true},
		{"gcp metadata trailing dot", PlanLocal, "http://metadata.google.internal.", "", true},
		{"short metadata name", PlanLocal, "http://metadata", "", true},
		{"ec2 instance data", PlanLocal, "http://instance-data", "", true},

		{"decimal ip", PlanLocal, "http://2130706433", "", true},
		{"hex ip", PlanLocal, "http://0x7f000001", "", true},
		{"dotted hex ip", PlanLocal, "http://0x7f.0.0.1", "", true},
		{"octal ip", PlanLocal, "http://0177.0.0.1", "", true},
		{"short ip", PlanLocal, "http://127.1", "", true},
		{"underscore host ok", PlanMetered, "https://my_host.example.com", "https://my_host.example.com", false},
		{"unicode host", PlanMetered, "https://exämple.com", "", true},
		{"space in host", PlanMetered, "https://exa mple.com", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CleanBaseURL(tt.plan, tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CleanBaseURL(%q, %q) error = %v, want error %v", tt.plan, tt.in, err, tt.wantErr)
			}
			if err != nil {
				if _, ok := err.(Invalid); !ok {
					t.Fatalf("error is %T, want Invalid", err)
				}
				if strings.Contains(err.Error(), tt.in) && tt.in != "" {
					t.Fatalf("error text repeats the input: %v", err)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("CleanBaseURL(%q, %q) = %q, want %q", tt.plan, tt.in, got, tt.want)
			}
		})
	}
}

func TestAddrReason(t *testing.T) {
	tests := []struct {
		plan, addr string
		blocked    bool
	}{
		{PlanMetered, "8.8.8.8", false},
		{PlanMetered, "127.0.0.1", true},
		{PlanMetered, "10.1.1.1", true},
		{PlanMetered, "169.254.169.254", true},
		{PlanSubscription, "192.168.0.1", true},
		{PlanLocal, "127.0.0.1", false},
		{PlanLocal, "10.1.1.1", false},
		{PlanLocal, "169.254.169.254", true},
		{PlanLocal, "fe80::1", true},
		{PlanLocal, "100.100.100.200", true},
		{PlanLocal, "0.0.0.0", true},
	}
	for _, tt := range tests {
		got := AddrReason(tt.plan, netip.MustParseAddr(tt.addr))
		if (got != "") != tt.blocked {
			t.Errorf("AddrReason(%s, %s) = %q, blocked want %v", tt.plan, tt.addr, got, tt.blocked)
		}
	}
}

func TestCleanName(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"Work OpenAI", "Work OpenAI", false},
		{"  padded  ", "padded", false},
		{strings.Repeat("a", 60), strings.Repeat("a", 60), false},
		{strings.Repeat("é", 60), strings.Repeat("é", 60), false},
		{"", "", true},
		{"   ", "", true},
		{strings.Repeat("a", 61), "", true},
		{"bad\x00name", "", true},
		{"two\nlines", "", true},
		{"tab\there", "", true},
		{"bad\xffutf8", "", true},
	}
	for _, tt := range tests {
		got, err := CleanName(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("CleanName(%q) = %q, %v", tt.in, got, err)
		}
	}
}

func TestCleanModels(t *testing.T) {
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "m" + strings.Repeat("x", i)
		}
		return out
	}
	tests := []struct {
		name    string
		in      []string
		want    int
		wantErr bool
	}{
		{"none", nil, 0, false},
		{"some", []string{"a", " b "}, 2, false},
		{"hundred", many(100), 100, false},
		{"hundred and one", many(101), 0, true},
		{"longest", []string{strings.Repeat("a", 120)}, 1, false},
		{"too long", []string{strings.Repeat("a", 121)}, 0, true},
		{"empty", []string{""}, 0, true},
		{"blank", []string{"  "}, 0, true},
		{"duplicate", []string{"a", "b", "a"}, 0, true},
		{"duplicate after trim", []string{"a", " a"}, 0, true},
		{"control", []string{"a\x00"}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CleanModels(tt.in)
			if (err != nil) != tt.wantErr || len(got) != tt.want {
				t.Fatalf("CleanModels = %v, %v", got, err)
			}
		})
	}
	if got, _ := CleanModels(nil); got == nil {
		t.Fatal("an empty list should be empty, not nil")
	}
	if got, _ := CleanModels([]string{" a "}); got[0] != "a" {
		t.Fatalf("model not trimmed: %q", got[0])
	}
}

func TestCleanKeyAndNeedKey(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain", "sk-abc123", "sk-abc123", false},
		{"trimmed", "  sk-abc\n", "sk-abc", false},
		{"empty", "", "", false},
		{"max", strings.Repeat("k", 4096), strings.Repeat("k", 4096), false},
		{"too long", strings.Repeat("k", 4097), "", true},
		{"inner newline", "sk-a\nb", "", true},
		{"nul", "sk-a\x00b", "", true},
		{"del", "sk-a\x7fb", "", true},
		{"bad utf8", "sk-\xff", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CleanKey(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("CleanKey = %q, %v", got, err)
			}
			if err != nil && tt.in != "" && strings.Contains(err.Error(), strings.TrimSpace(tt.in)) {
				t.Fatalf("error text repeats the key: %v", err)
			}
		})
	}
	needs := []struct {
		plan, key string
		saved     bool
		wantErr   bool
	}{
		{PlanMetered, "", false, true},
		{PlanSubscription, "", false, true},
		{PlanMetered, "k", false, false},
		{PlanMetered, "", true, false},
		{PlanLocal, "", false, false},
		{PlanLocal, "k", false, false},
	}
	for _, tt := range needs {
		if err := NeedKey(tt.plan, tt.key, tt.saved); (err != nil) != tt.wantErr {
			t.Errorf("NeedKey(%s, %q, %v) = %v", tt.plan, tt.key, tt.saved, err)
		}
	}
}

func TestCleanPlanAndLabel(t *testing.T) {
	for _, p := range []string{PlanMetered, PlanSubscription, PlanLocal} {
		if got, err := CleanPlan(p); err != nil || got != p {
			t.Errorf("CleanPlan(%q) = %q, %v", p, got, err)
		}
	}
	for _, p := range []string{"", "free", "Metered", "local "} {
		if _, err := CleanPlan(p); err == nil {
			t.Errorf("CleanPlan(%q) accepted", p)
		}
	}
	if _, err := CleanLabel(""); err == nil {
		t.Error("empty label accepted")
	}
	if _, err := CleanLabel(strings.Repeat("a", 61)); err == nil {
		t.Error("long label accepted")
	}
	if got, err := CleanLabel(" openai "); err != nil || got != "openai" {
		t.Errorf("CleanLabel = %q, %v", got, err)
	}
}

func TestCheckScripted(t *testing.T) {
	tests := []struct {
		name    string
		adapter string
		models  []string
		wantErr bool
	}{
		{"fake adapter may list one", AdapterFake, []string{"fake-ok", "plain"}, false},
		{"real adapter without one", AdapterOpenAI, []string{"gpt-x", "faker"}, false},
		{"real adapter with one", AdapterOpenAI, []string{"gpt-x", "fake-ok"}, true},
		{"real adapter with an unlisted fake id", AdapterOpenAI, []string{"fake-anything"}, true},
		{"an unknown adapter counts as real", "other", []string{"fake-slow"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckScripted(tt.adapter, tt.models)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckScripted(%q, %v) = %v", tt.adapter, tt.models, err)
			}
		})
	}
}
