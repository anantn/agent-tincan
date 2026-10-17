package client_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
)

// Handback reads the relay's capabilities once and keeps a good read, but
// a failed read is not kept: the next handback reads them again.
func TestHandbackCapabilityCache(t *testing.T) {
	var reads, posts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/capabilities":
			if reads.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":"no"}`)
				return
			}
			_, _ = io.WriteString(w, `{"handback":true}`)
		case strings.HasSuffix(r.URL.Path, "/handback"):
			posts.Add(1)
			_, _ = io.WriteString(w, `{"outcome":"moved","to":"muse"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	r := client.NewRelayHTTP(ts.URL, ts.Client())
	until := time.Now().Add(time.Hour)
	if _, err := r.Handback(t.Context(), "r1", until); err == nil {
		t.Fatal("a failed capability read handed back")
	}
	for range 2 {
		out, err := r.Handback(t.Context(), "r1", until)
		if err != nil || out.Outcome != client.HandbackMoved || out.To != "muse" {
			t.Fatalf("handback = %+v, %v", out, err)
		}
	}
	if reads.Load() != 2 || posts.Load() != 2 {
		t.Fatalf("capability reads = %d, handbacks = %d; want 2 and 2", reads.Load(), posts.Load())
	}
}
