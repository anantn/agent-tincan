package wake

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvanhorn/agent-tincan/internal/identity"
)

// SetConfigPath enables admin updates to this relay's private wake file.
// Call it before serving requests.
func (w *Waker) SetConfigPath(path string) { w.configPath = path }

func (w *Waker) target(agent string) Target {
	w.cfgMu.RLock()
	defer w.cfgMu.RUnlock()
	return w.cfg[agent]
}

// WakeMethods is safe to show to an admin: no addresses or credentials.
func (w *Waker) WakeMethods() map[string]string {
	w.cfgMu.RLock()
	defer w.cfgMu.RUnlock()
	out := make(map[string]string, len(w.cfg))
	for name, t := range w.cfg {
		out[name] = t.Method
	}
	return out
}

// ConfigureWebhook persists before publishing a fresh immutable config snapshot.
// Other agents and the target's authentication, fallback and rate options survive.
func (w *Waker) ConfigureWebhook(agent, endpoint, token string, setToken bool) error {
	if !identity.ValidName(agent) {
		return errors.New("invalid agent name")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return errors.New("webhook must be an absolute http(s) URL without user info or fragment")
	}
	if strings.ContainsAny(token, "\r\n\x00") || len(token) > 8192 {
		return errors.New("invalid bearer token")
	}
	w.cfgMu.Lock()
	defer w.cfgMu.Unlock()
	if w.configPath == "" {
		return errors.New("wake configuration updates are unavailable on this relay")
	}
	cfg, err := LoadConfig(w.configPath)
	if err != nil {
		return errors.New("cannot read wake.json; check its JSON and permissions on the relay")
	}
	if cfg == nil {
		cfg = Config{}
	}
	t := cfg[agent]
	if t.Method == Email {
		return errors.New("changing an email wake needs a relay restart; edit wake.json to change its method")
	}
	t.Method, t.URL = Webhook, endpoint
	if setToken {
		t.BearerToken = token
	}
	if err := checkTarget(t); err != nil {
		return errors.New("invalid webhook settings")
	}
	cfg[agent] = t
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return errors.New("cannot encode wake configuration")
	}
	if err := writeWakeConfig(w.configPath, append(raw, '\n')); err != nil {
		return errors.New("cannot save wake.json on the relay; live settings were not changed")
	}
	w.cfg = cfg
	return nil
}

func writeWakeConfig(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".wake-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
