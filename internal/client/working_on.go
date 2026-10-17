package client

import (
	"context"
	"errors"
	"net/http"
)

// ErrWorkingOnUnsupported identifies a relay that predates working-on lines.
var ErrWorkingOnUnsupported = errors.New("relay does not support working-on lines; upgrade the relay")

// ReportWorkingOn sets this session's working-on line to text. The relay
// files it under the authenticated agent and this client's session, so a
// client can only ever write its own line, and drops it if no report
// refreshes it within the relay's session grace.
func (r *Relay) ReportWorkingOn(ctx context.Context, text string) error {
	return workingOnErr(r.call(ctx, r.api, "PUT", "/v1/agents/self/working-on", map[string]string{"text": text}, nil))
}

// ClearWorkingOn removes this session's working-on line.
func (r *Relay) ClearWorkingOn(ctx context.Context) error {
	return workingOnErr(r.call(ctx, r.api, "DELETE", "/v1/agents/self/working-on", nil, nil))
}

func workingOnErr(err error) error {
	if IsStatus(err, http.StatusNotFound) || IsStatus(err, http.StatusMethodNotAllowed) {
		return ErrWorkingOnUnsupported
	}
	return err
}
