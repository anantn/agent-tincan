package pass

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/history"
)

// tincan pass install writes a launchd agent (a systemd user unit on
// Linux) that runs tincan pass serve as the agent in pass.json, and
// starts nothing.
func TestInstallService(t *testing.T) {
	for goos, want := range map[string][]string{
		"darwin": {"Library/LaunchAgents/com.agenttincan.pass.plist", "<string>pass</string>\n    <string>serve</string>", "/.config/tincan/pass.json", "Library/Logs/tincan-pass.log"},
		"linux":  {".config/systemd/user/tincan-pass.service", `" pass serve`, "%h/.config/tincan/pass.json"},
	} {
		home := t.TempDir()
		res, err := InstallService(history.ServiceOptions{GOOS: goos, Home: home, Binary: "/opt/tincan", UID: 501})
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if !strings.HasPrefix(res.Path, home) || !strings.HasSuffix(res.Path, filepath.FromSlash(want[0])) {
			t.Fatalf("%s: path %s", goos, res.Path)
		}
		b, err := os.ReadFile(res.Path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want[1:] {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s: definition missing %q:\n%s", goos, w, b)
			}
		}
		if strings.Contains(string(b), "__") || strings.Contains(string(b), "council") {
			t.Errorf("%s: definition has a placeholder or council left:\n%s", goos, b)
		}
	}
}
