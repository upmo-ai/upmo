package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + file)
		}
		dir = parent
	}
}

func readRepoFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(moduleRoot(t), rel)) //nolint:gosec // rel is a fixed path in this module
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestImagePinLines(t *testing.T) {
	pinned := "image: golang:1.26.11-bookworm@sha256:ea47ee45f1284d60f9cc09cb2c3bb4f508c256b640bd8bb30342ff13d1adcb52\n"
	tests := []struct {
		name string
		file string
		body string
		want string
	}{
		{
			name: "tag only",
			file: "compose.yml",
			body: "image: golang:1.26.11-bookworm\n",
			want: "compose.yml:1: image: golang:1.26.11-bookworm lacks @sha256:",
		},
		{
			name: "folded scalar",
			file: "compose.yml",
			body: "image: >\n  alpine:3\n",
			want: "compose.yml:1: image: > lacks @sha256:",
		},
		{
			name: "lowercase from",
			file: "Dockerfile",
			body: "from alpine:3\n",
			want: "Dockerfile:1: from alpine:3 lacks @sha256:",
		},
		{
			name: "platform flag tag only",
			file: "Dockerfile",
			body: "FROM --platform=linux/amd64 alpine:3 AS base\n",
			want: "Dockerfile:1: FROM --platform=linux/amd64 alpine:3 AS base lacks @sha256:",
		},
		{
			name: "platform flag pinned",
			file: "Dockerfile",
			body: "FROM --platform=linux/amd64 debian:bookworm@sha256:e67eeead736ff7bbe258ef088bff683635c561b1fcfefad0806c1df4ab49ce0e AS base\nFROM base\n",
		},
		{
			name: "sandbox build tag",
			file: "compose.yml",
			body: "image: upmo/sandbox:base\n",
		},
		{
			name: "markitdown build tag",
			file: "compose.yml",
			body: "image: upmo/markitdown:dev\n",
		},
		{
			name: "digest pin",
			file: "compose.yml",
			body: pinned,
		},
		{
			name: "first unpinned line",
			file: "compose.yml",
			body: "image: golang:1.26.11-bookworm\nimage: pgvector/pgvector:0.8.7-pg18-bookworm\n",
			want: "compose.yml:1: image: golang:1.26.11-bookworm lacks @sha256:",
		},
		{
			name: "from stage",
			file: "deploy/sandbox/Dockerfile",
			body: "FROM debian:bookworm-slim@sha256:e67eeead736ff7bbe258ef088bff683635c561b1fcfefad0806c1df4ab49ce0e AS base\nFROM base AS cli\n",
		},
		{
			name: "from tag",
			file: "deploy/sandbox/Dockerfile",
			body: "FROM debian:bookworm-slim\n",
			want: "deploy/sandbox/Dockerfile:1: FROM debian:bookworm-slim lacks @sha256:",
		},
		{
			name: "syntax tag",
			file: "deploy/markitdown/Dockerfile",
			body: "# syntax=docker/dockerfile:1\n",
			want: "deploy/markitdown/Dockerfile:1: # syntax=docker/dockerfile:1 lacks @sha256:",
		},
		{
			name: "syntax pin",
			file: "deploy/markitdown/Dockerfile",
			body: "# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unpinnedImage(tt.file, []byte(tt.body))
			if got != tt.want {
				t.Fatalf("unpinnedImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRepoImagePins(t *testing.T) {
	for _, rel := range []string{
		"compose.yml",
		"deploy/sandbox/Dockerfile",
		"deploy/markitdown/Dockerfile",
	} {
		if got := unpinnedImage(rel, readRepoFile(t, rel)); got != "" {
			t.Errorf("%s", got)
		}
	}
}
