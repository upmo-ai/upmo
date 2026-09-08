package domain

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	awsKey := "AKIA" + strings.Repeat("A", 16)
	googleKey := "AIza" + strings.Repeat("a", 35)
	privateKey := "-----BEGIN PRIVATE KEY-----\nmaterial\n-----END PRIVATE KEY-----"
	urlWithUserinfo := strings.Join([]string{"https://alice", "s3cret@example.test/path"}, ":")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "url userinfo",
			in:   "Open " + urlWithUserinfo,
			want: "Open https://***@example.test/path",
		},
		{
			name: "bearer value",
			in:   "Authorization header is Bearer abcDEF123456",
			want: "Authorization header is Bearer ***",
		},
		{
			name: "token value",
			in:   "token Abcdefghijk1",
			want: "token ***",
		},
		{
			name: "keyed secret",
			in:   "client_secret: Upper2case",
			want: "client_secret: ***",
		},
		{
			name: "aws access key",
			in:   "key " + awsKey,
			want: "key ***",
		},
		{
			name: "google api key",
			in:   "key " + googleKey,
			want: "key ***",
		},
		{
			name: "private key",
			in:   "before\n" + privateKey + "\nafter",
			want: "before\n***\nafter",
		},
		{
			name: "jwt",
			in:   "session eyJheader.payload.signature",
			want: "session ***",
		},
		{
			name: "email address",
			in:   "Write to ada@example.com today",
			want: "Write to *** today",
		},
		{
			name: "ordinary token prose",
			in:   "the token refreshes",
			want: "the token refreshes",
		},
		{
			name: "ordinary keyed prose",
			in:   "password: lowercase",
			want: "password: lowercase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Redact(tt.in); got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPrepare(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{
			name:   "trims text",
			in:     "  durable preference  ",
			want:   "durable preference",
			wantOK: true,
		},
		{
			name:   "strips fenced code",
			in:     "Remember this preference\n```go\nfmt.Println(\"ignored\")\n```\nfor future summaries",
			want:   "Remember this preference\n\nfor future summaries",
			wantOK: true,
		},
		{
			name:   "drops short remainder",
			in:     "keep\n```text\nlong fenced content that does not count\n```",
			wantOK: false,
		},
		{
			name:   "strips fence before limit",
			in:     "```text\n" + strings.Repeat("x", 5000) + "\n```\nRemember this durable preference",
			want:   "Remember this durable preference",
			wantOK: true,
		},
		{
			name:   "limits unicode runes",
			in:     strings.Repeat("é", 4001),
			want:   strings.Repeat("é", 4000),
			wantOK: true,
		},
		{
			name:   "cuts at separator in final window",
			in:     strings.Repeat("a", 3930) + " " + strings.Repeat("b", 100),
			want:   strings.Repeat("a", 3930),
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Prepare(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("Prepare() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("Prepare() = %q, want %q", got, tt.want)
			}
		})
	}
}
