package history

import (
	"path/filepath"
	"testing"
)

const (
	ccT1 = "77777777-7777-4777-8777-777777777777" // two ai-titles
	ccT2 = "88888888-8888-4888-8888-888888888888" // ai-title, then custom-title
	ccT3 = "99999999-9999-4999-8999-999999999999" // custom-title, then a newer ai-title
)

func TestClaudeCodeSessionTitle(t *testing.T) {
	r := &ClaudeCode{Root: filepath.Join(copyTree(t, "claudecode"), "projects")}
	for _, tc := range []struct {
		name, id, want string
	}{
		{"later ai-title wins", ccT1, "Socket wake for idle sessions"},
		{"custom-title beats an earlier ai-title", ccT2, "Named by hand"},
		{"custom-title beats a newer ai-title", ccT3, "Kept name"},
		{"prompts are never a title", ccS6, ""},
		{"unknown session", "00000000-0000-4000-8000-000000000000", ""},
	} {
		if got := r.SessionTitle(tc.id); got != tc.want {
			t.Errorf("%s: SessionTitle(%s) = %q, want %q", tc.name, tc.id, got, tc.want)
		}
	}
}

func TestClaudeCodeSessionTitleRejectsBadID(t *testing.T) {
	r := &ClaudeCode{Root: filepath.Join(copyTree(t, "claudecode"), "projects")}
	// Each would name a real transcript if joined into a path.
	for _, id := range []string{"", "../-Users-matt-code-z/" + ccT1, "*", "-Users-matt-code-z/" + ccT1} {
		if got := r.SessionTitle(id); got != "" {
			t.Errorf("SessionTitle(%q) = %q, want empty", id, got)
		}
	}
}

func TestClaudeCodeSessionTitleHonorsConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", copyTree(t, "claudecode"))
	if got := NewClaudeCode().SessionTitle(ccT2); got != "Named by hand" {
		t.Fatalf("SessionTitle = %q, want %q", got, "Named by hand")
	}
}
