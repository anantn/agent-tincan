package relay

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/wake"
)

func TestWakeSettingsAdminOnlyAndLive(t *testing.T) {
	h := newHarness(t, Config{})
	p := filepath.Join(t.TempDir(), "wake.json")
	w := wake.New(wake.Config{}, nil, wake.Options{})
	defer w.Stop()
	w.SetConfigPath(p)
	h.srv.SetWakeSettings(w)
	h.srv.SetWakeNamer(w)
	body := `{"webhook":"https://example.com/private-hook","bearer_token":"private-token"}`
	h.do(grokAddr, "PUT", "/v1/admin/wake-config/grokbot", body, http.StatusForbidden, nil)
	h.do(grokAddr, "GET", "/v1/admin/wake-config", "", http.StatusForbidden, nil)
	h.do(macAddr, "PUT", "/v1/admin/wake-config/missing", body, http.StatusNotFound, nil)
	h.do(macAddr, "PUT", "/v1/admin/wake-config/grokbot", body, http.StatusOK, nil)
	var methods map[string]string
	h.do(macAddr, "GET", "/v1/admin/wake-config", "", http.StatusOK, &methods)
	if methods["grokbot"] != "webhook" || w.WakeMethod("grokbot") != "webhook" {
		t.Fatal(methods)
	}
	raw, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(raw), "private-token") {
		t.Fatalf("not persisted: %v", err)
	}
	h.do(macAddr, "PUT", "/v1/admin/wake-config/grokbot", `{"webhook":"file:///private-token"}`, http.StatusBadRequest, nil)
	h.do(macAddr, "PUT", "/v1/admin/wake-config/grokbot", `{"webhook":`, http.StatusBadRequest, nil)
	h.do(macAddr, "PUT", "/v1/admin/wake-config/grokbot", `{"webhook":"https://example.com","unknown":"private-token"}`, http.StatusBadRequest, nil)
}
