package history

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// SessionTitle returns the title of one Claude Code session: its latest
// custom title, else its latest generated title, else "". It reads only
// title records, so unlike parse it never falls back to a prompt. An id
// that is not a plain transcript name returns "" before any file is
// opened.
func (c *ClaudeCode) SessionTitle(sessionID string) string {
	if !idPattern.MatchString(sessionID) {
		return ""
	}
	projects, err := os.ReadDir(c.Root)
	if err != nil {
		return ""
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		path := filepath.Join(c.Root, p.Name(), sessionID+".jsonl")
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			continue
		}
		return claudeTitle(path)
	}
	return ""
}

// claudeTitle streams one transcript for its title records.
func claudeTitle(path string) string {
	var aiTitle, customTitle string
	_ = scanFile(path, func(line []byte) bool {
		// Skip decoding the many records that cannot be titles.
		if !bytes.Contains(line, []byte(`-title"`)) {
			return true
		}
		var r claudeRecord
		if json.Unmarshal(line, &r) != nil {
			return true
		}
		switch r.Type {
		case "ai-title":
			aiTitle = r.AITitle
		case "custom-title":
			customTitle = r.CustomTitle
		}
		return true
	})
	if customTitle != "" {
		return customTitle
	}
	return aiTitle
}
