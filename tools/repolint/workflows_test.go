package main

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDependencyReviewOnMain(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Name string         `yaml:"name"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(readRepoFile(t, ".github/workflows/ci.yml"), &wf); err != nil {
		t.Fatal(err)
	}
	job, ok := wf.Jobs["dependency-review"]
	if !ok {
		t.Fatal("dependency-review job missing")
	}
	const wantIf = "github.event_name == 'pull_request' || (github.event_name == 'push' && github.ref == 'refs/heads/main')"
	if job.If != wantIf {
		t.Fatalf("if = %q, want %q", job.If, wantIf)
	}
	var with map[string]any
	for _, step := range job.Steps {
		if step.Name == "Review Dependencies" {
			with = step.With
			break
		}
	}
	if with == nil {
		t.Fatal("Review Dependencies step missing")
	}
	want := map[string]string{
		"fail-on-severity":      "high",
		"comment-summary-in-pr": "never",
		"base-ref":              "${{ github.event.pull_request.base.sha || github.event.before }}",
		"head-ref":              "${{ github.event.pull_request.head.sha || github.sha }}",
	}
	for key, value := range want {
		got, _ := with[key].(string)
		if got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
}
