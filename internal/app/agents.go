package app

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HookAgents (re)loads chezmoi-managed LaunchAgents on macOS or enables systemd user timers on Linux.
func HookAgents(d *Data) error {
	managed := strings.Split(Out("chezmoi", "managed", "--include", "files", "--path-style", "absolute"), "\n")
	if !IsMac {
		var timers []string
		for _, p := range managed {
			if strings.Contains(p, "/.config/systemd/user/") && strings.HasSuffix(p, ".timer") {
				timers = append(timers, filepath.Base(p))
			}
		}
		if len(timers) > 0 && Has("systemctl") {
			_ = Run("systemctl", "--user", "daemon-reload")
			for _, t := range timers {
				_ = Run("systemctl", "--user", "enable", "--now", t)
			}
		}
		return nil
	}
	uid := fmt.Sprint(os.Getuid())
	var hashes map[string]string
	statePath := filepath.Join(StateDir, "agents.json")
	_ = readJSON(statePath, &hashes)
	if hashes == nil {
		hashes = map[string]string{}
	}
	for _, p := range managed {
		if !strings.Contains(p, "/Library/LaunchAgents/") || !strings.HasSuffix(p, ".plist") {
			continue
		}
		label := strings.TrimSuffix(filepath.Base(p), ".plist")
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(b))
		loaded := OK("launchctl", "print", "gui/"+uid+"/"+label)
		if loaded && hashes[label] == sum {
			continue
		}
		_ = Run("launchctl", "bootout", "gui/"+uid+"/"+label)
		if err := Run("launchctl", "bootstrap", "gui/"+uid, p); err != nil {
			Warn("load %s: %v", label, firstLine(err.Error()))
			continue
		}
		hashes[label] = sum
		Say("loaded %s", label)
	}
	// Agents that are no longer managed (e.g. auto-sync turned off) get unloaded.
	for label := range hashes {
		if !Exists(filepath.Join(Home, "Library/LaunchAgents", label+".plist")) {
			_ = Run("launchctl", "bootout", "gui/"+uid+"/"+label)
			delete(hashes, label)
			Say("unloaded %s", label)
		}
	}
	return writeJSON(statePath, hashes)
}
