package domain

import (
	"strings"
	"testing"
)

func TestNeverFromChatListBlocksAllForbiddenDomains(t *testing.T) {
	if !IsNeverFromChat("") || !IsNeverFromChat("   ") {
		t.Fatal("empty tool name must be forbidden in chat")
	}

	for _, prefix := range ForbiddenChatToolPrefixes {
		name := prefix + "action"
		for _, variant := range []string{name, strings.ToUpper(name), "  " + name + "  "} {
			if !IsNeverFromChat(variant) {
				t.Errorf("IsNeverFromChat(%q) = false, want true (prefix %q)", variant, prefix)
			}
		}
	}

	for name := range ForbiddenChatToolNames {
		for _, variant := range []string{name, strings.ToUpper(name), "  " + name + "  "} {
			if !IsNeverFromChat(variant) {
				t.Errorf("IsNeverFromChat(%q) = false, want true (exact name)", variant)
			}
		}
	}

	for name := range AllowedDirectChatTools {
		if IsNeverFromChat(name) {
			t.Errorf("IsNeverFromChat(%q) = true, want false for allowed direct chat tool", name)
		}
	}

	for _, allowed := range []string{"github_list_issues", "slack_post_message", "linear_create_issue"} {
		if IsNeverFromChat(allowed) {
			t.Errorf("IsNeverFromChat(%q) = true, want false for benign connector tool", allowed)
		}
	}
}
