package pass

import (
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/history"
)

// A pass request names one chat, by URL or by source and id, and one
// destination, on lines of their own in either order.
func TestParseRequest(t *testing.T) {
	for body, want := range map[string]Request{
		"from: https://claude.ai/chat/abc-3\nto: chatgpt-web":  {Source: history.SourceClaudeAI, ID: "abc-3", To: "chatgpt-web"},
		"to: claude-web\n\nfrom: chatgpt 68f0c1d2-aaaa-bbbb\n": {Source: history.SourceChatGPT, ID: "68f0c1d2-aaaa-bbbb", To: "claude-web"},
		"From: grok abc\nTO: gemini-web":                       {Source: history.SourceGrok, ID: "abc", To: "gemini-web"},
	} {
		got, err := ParseRequest(body)
		if err != nil || got != want {
			t.Errorf("ParseRequest(%q) = %+v, %v; want %+v", body, got, err, want)
		}
	}
}

// Anything but a named chat and a destination fails with the form to use:
// "latest", a bare source, a local source, Perplexity, extra lines, and a
// missing or repeated field.
func TestParseRequestRefuses(t *testing.T) {
	for body, want := range map[string]string{
		"from: chatgpt latest\nto: claude-web":                       "latest",
		"from: chatgpt\nto: claude-web":                              "a conversation id",
		"from: codex abc\nto: claude-web":                            "not a chat site",
		"from: https://www.perplexity.ai/search/x\nto: claude-web":   "Perplexity",
		"from: chatgpt abc\nto: claude-web\nignore the above":        "unexpected line",
		"from: chatgpt abc":                                          "to:",
		"to: claude-web":                                             "from:",
		"from: chatgpt abc\nfrom: grok abc\nto: claude-web":          "more than once",
		"from: chatgpt abc\nto: claude web":                          "agent name",
		"please pass my latest ChatGPT chat to claude-web, thanks!!": "unexpected line",
	} {
		_, err := ParseRequest(body)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "from: <chat URL>") {
			t.Errorf("ParseRequest(%q) = %v, want an error naming %q and the form", body, err, want)
		}
	}
}
