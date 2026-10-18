package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// A question mirrored into a web member's context carries the mention's
// request id, so the poster can answer it without digging in the inbox.
func TestStatusTagQuestionNamesRequest(t *testing.T) {
	q := envelope.RoomPost{Status: envelope.StatusNeedsInput, ReplyTo: "r42"}
	if got := statusTag(q); got != ", question, answer with request r42" {
		t.Fatalf("question tag = %q", got)
	}
	if got := statusTag(envelope.RoomPost{Status: envelope.StatusNeedsInput}); got != ", question" {
		t.Fatalf("question without a request = %q", got)
	}
	if got := statusTag(envelope.RoomPost{Status: envelope.StatusFailed, ReplyTo: "r1"}); got != ", failed" {
		t.Fatalf("failed tag = %q", got)
	}
	got := packRoomContext("head", "launch", []envelope.RoomPost{{Author: "codex", Body: "which branch?", CreatedAt: time.Now(), Status: envelope.StatusNeedsInput, ReplyTo: "r42"}}, 4096)
	if !strings.Contains(got, "codex, question, answer with request r42]") {
		t.Fatalf("context lacks the request id:\n%s", got)
	}
}
