package wake

import (
	"encoding/json"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigureWebhookPersistsAndApplies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wake.json")
	if err := os.WriteFile(p, []byte(`{"other":{"method":"wait"},"grokbot":{"method":"webhook","url":"https://old.example/hook","hmac_secret":"keep-me","max_per_hour":3}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	w := New(cfg, nil, Options{})
	defer w.Stop()
	w.SetConfigPath(p)
	if err := w.ConfigureWebhook("grokbot", "https://new.example/hook", "private-token", true); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if saved["other"].Method != Wait || saved["grokbot"].HMACSecret != "keep-me" || saved["grokbot"].MaxPerHour != 3 || saved["grokbot"].BearerToken != "private-token" {
		t.Fatalf("lost settings: %#v", saved)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 30 {
				_ = w.WakeMethod("grokbot")
				_ = w.CheckEvery("other")
				_ = w.WakeMethods()
			}
		})
	}
	for range 5 {
		if err := w.ConfigureWebhook("grokbot", "http://localhost:8080/hook", "", false); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if w.WakeMethod("grokbot") != Webhook || w.target("grokbot").BearerToken != "private-token" {
		t.Fatal("not applied or token lost")
	}
	for _, bad := range []string{"file:///tmp/key", "https://user:secret@example.com", "https://", "https://example.com/#secret"} {
		if err := w.ConfigureWebhook("grokbot", bad, "", false); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe URL or error: %v", err)
		}
	}
	before, _ := os.ReadFile(p)
	if err := w.ConfigureWebhook("bad/name", "https://example.com", "", false); err == nil {
		t.Fatal("bad name accepted")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("failed update changed file")
	}
}

func TestConfigureWebhookFailureKeepsLiveConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "wake.json")
	w := New(Config{"agent": {Method: None}}, nil, Options{})
	defer w.Stop()
	w.SetConfigPath(path)
	if err := w.ConfigureWebhook("agent", "https://example.com/hook", "secret-value", true); err == nil {
		t.Fatal("missing directory accepted")
	}
	if w.WakeMethod("agent") != None {
		t.Fatal("failed save changed live config")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := w.ConfigureWebhook("agent", "https://example.com/hook", "secret-value", true); err != nil {
		t.Fatal(err)
	}
	if w.WakeMethod("agent") != Webhook {
		t.Fatal("none to webhook not applied")
	}
	if err := w.ConfigureWebhook("agent", "https://example.com/hook", "", true); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg["agent"].BearerToken != "" {
		t.Fatal("empty token did not clear")
	}
	if err := os.WriteFile(path, []byte("broken secret-value"), 0600); err != nil {
		t.Fatal(err)
	}
	err = w.ConfigureWebhook("agent", "https://example.com/hook", "", false)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("bad error: %v", err)
	}
	if w.WakeMethod("agent") != Webhook {
		t.Fatal("invalid disk config changed runtime")
	}
}

func TestConfigureWebhookChangesActualWakeDestination(t *testing.T) {
	old, newDest := &recorder{}, &recorder{}
	oldServer, newServer := old.server(t), newDest.server(t)
	path := filepath.Join(t.TempDir(), "wake.json")
	cfg := Config{"agent": {Method: Webhook, URL: oldServer.URL}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	w := New(cfg, nil, Options{Debounce: time.Millisecond})
	defer w.Stop()
	w.SetConfigPath(path)
	if err := w.ConfigureWebhook("agent", newServer.URL+"/updated", "new-token", true); err != nil {
		t.Fatal(err)
	}
	w.Queued(t.Context(), envelope.Request{ID: "request", To: "agent", Kind: envelope.KindAsk})
	deadline := time.Now().Add(3 * time.Second)
	for newDest.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if old.count() != 0 || newDest.count() != 1 {
		t.Fatalf("old=%d new=%d", old.count(), newDest.count())
	}
	if newDest.path(0) != "/updated" || newDest.auth[0] != "Bearer new-token" {
		t.Fatal("new settings not used by webhook")
	}
}
