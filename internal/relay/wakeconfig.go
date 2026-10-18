package relay

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/mvanhorn/agent-tincan/internal/identity"
)

// WakeSettings owns the relay-local private configuration. Only its method
// names cross back to an admin; ordinary agents cannot read or change it.
type WakeSettings interface {
	WakeMethods() map[string]string
	ConfigureWebhook(agent, endpoint, token string, setToken bool) error
}

func (s *Server) SetWakeSettings(settings WakeSettings) { s.wakeSettings = settings }

func (s *Server) handleWakeSettings(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeErr(w, http.StatusForbidden, identity.ErrNotAdmin)
		return
	}
	if s.wakeSettings == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("this relay does not support wake configuration; upgrade the relay"))
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.wakeSettings.WakeMethods())
		return
	}
	name := r.PathValue("agent")
	if !identity.ValidName(name) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid agent name"))
		return
	}
	if _, ok, err := s.dir.Agent(r.Context(), name); err != nil || !ok {
		writeErr(w, http.StatusNotFound, errors.New("agent is not joined"))
		return
	}
	var in struct {
		Webhook     string  `json:"webhook"`
		BearerToken *string `json:"bearer_token,omitempty"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("body must contain webhook and optional bearer_token"))
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, errors.New("body must contain one JSON object"))
		return
	}
	token := ""
	if in.BearerToken != nil {
		token = *in.BearerToken
	}
	if err := s.wakeSettings.ConfigureWebhook(name, in.Webhook, token, in.BearerToken != nil); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"agent": name, "method": "webhook"})
}
