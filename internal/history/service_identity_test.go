package history

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebServiceCustomIdentity(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			cfg := filepath.Join(home, "my configs", "dayward.json")
			r, err := InstallWebService(SourceClaudeAI, ServiceOptions{GOOS: goos, Home: home, Binary: "/opt/tincan", AgentName: "dayward", ConfigPath: cfg})
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(r.Path)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"dayward", cfg, "--name"} {
				if !strings.Contains(string(b), want) {
					t.Fatalf("missing %q in %s", want, b)
				}
			}
			if !strings.Contains(r.Path, "dayward") {
				t.Fatalf("service collides: %s", r.Path)
			}
		})
	}
	for _, bad := range []string{"../escape", "bad name", "bad%name"} {
		if _, err := InstallWebService(SourceClaudeAI, ServiceOptions{GOOS: "linux", Home: t.TempDir(), Binary: "/opt/tincan", AgentName: bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestCustomWebNameIsSafeInPlistComments(t *testing.T) {
	r, err := InstallWebService(SourceClaudeAI, ServiceOptions{GOOS: "darwin", Home: t.TempDir(), Binary: "/opt/tincan", AgentName: "day--ward"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	var plist any
	if err := xml.Unmarshal(raw, &plist); err != nil {
		t.Fatalf("invalid plist: %v", err)
	}
}
