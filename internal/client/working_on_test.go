package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ReportWorkingOn sends the title as the session's own line, and
// ClearWorkingOn removes it, both naming the session.
func TestReportWorkingOnSendsSessionTitle(t *testing.T) {
	type call struct{ method, session, text string }
	var calls []call
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/self/working-on" {
			t.Errorf("path %s", r.URL.Path)
		}
		var in struct{ Text string }
		if r.Method == "PUT" {
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode: %v", err)
			}
		}
		calls = append(calls, call{r.Method, r.Header.Get(SessionHeader), in.Text})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	r, err := NewRelay(ts.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	r.SetSession("s1")
	if err := r.ReportWorkingOn(t.Context(), "Fix flaky relay test"); err != nil {
		t.Fatal(err)
	}
	if err := r.ClearWorkingOn(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []call{{"PUT", "s1", "Fix flaky relay test"}, {"DELETE", "s1", ""}}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
}

// A relay that predates the route answers 404 or 405, which both read as
// unsupported so the caller can stop reporting.
func TestReportWorkingOnUnsupported(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		r, err := NewRelay(ts.URL, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.ReportWorkingOn(t.Context(), "x"); !errors.Is(err, ErrWorkingOnUnsupported) {
			t.Errorf("PUT %d: err = %v", code, err)
		}
		if err := r.ClearWorkingOn(t.Context()); !errors.Is(err, ErrWorkingOnUnsupported) {
			t.Errorf("DELETE %d: err = %v", code, err)
		}
		ts.Close()
	}
}

// The working-on field is quoted, so a title with commas and field-like
// text stays one value, and other sessions show as a count.
func TestWorkingOnField(t *testing.T) {
	for _, c := range []struct {
		a    AgentInfo
		want string
	}{
		{AgentInfo{}, ""},
		{AgentInfo{WorkingOn: "Fix flaky relay test"}, `working_on="Fix flaky relay test"`},
		{AgentInfo{WorkingOn: `ship it, wake=none "now"`}, `working_on="ship it, wake=none \"now\""`},
		{AgentInfo{WorkingOn: "Fix flaky relay test", WorkingOnOthers: 2}, `working_on="Fix flaky relay test" (+2)`},
	} {
		if got := c.a.WorkingOnField(); got != c.want {
			t.Errorf("WorkingOnField(%+v) = %q, want %q", c.a, got, c.want)
		}
	}
}
