package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var zenFiles = []string{"user.js", "zen-themes.json", "zen-keyboard-shortcuts.json"}

func zenRoot() string {
	if IsMac {
		return filepath.Join(Home, "Library/Application Support/zen")
	}
	return filepath.Join(Home, ".zen")
}

// zenProfile is the [Install*] default profile whose prefs.js was touched most recently.
func zenProfile() string {
	root := zenRoot()
	b, err := os.ReadFile(filepath.Join(root, "profiles.ini"))
	if err != nil {
		return ""
	}
	best, bestT := "", int64(0)
	for _, m := range regexp.MustCompile(`(?m)^Default=(Profiles/.*)$`).FindAllStringSubmatch(string(b), -1) {
		p := filepath.Join(root, strings.TrimSpace(m[1]))
		if st, err := os.Stat(filepath.Join(p, "prefs.js")); err == nil && st.ModTime().Unix() > bestT {
			best, bestT = p, st.ModTime().Unix()
		}
	}
	return best
}

func zenRunning() bool { return OK("pgrep", "-xq", "zen") || OK("pgrep", "-x", "zen") }

// HookZen copies the repo's Zen config (.zen/ in the source dir) into the active profile.
func HookZen(d *Data) error {
	src := filepath.Join(d.Src(), ".zen")
	if !Exists(src) {
		return nil
	}
	prof := zenProfile()
	if prof == "" {
		Say("Zen not set up yet; open it once, then run `chezmoi apply`")
		return nil
	}
	if zenRunning() {
		Say("Zen is running; quit it and run `chezmoi apply` to update its config")
		return nil
	}
	for _, f := range zenFiles {
		if b, err := os.ReadFile(filepath.Join(src, f)); err == nil {
			_ = os.WriteFile(filepath.Join(prof, f), b, 0o644)
		}
	}
	if Exists(filepath.Join(src, "chrome")) {
		_ = os.MkdirAll(filepath.Join(prof, "chrome"), 0o755)
		_ = Run("cp", "-R", filepath.Join(src, "chrome")+"/.", filepath.Join(prof, "chrome")+"/")
	}
	Say("Zen config copied to %s", filepath.Base(prof))
	return nil
}

// captureZen copies Zen config back into the repo.
func captureZen(d *Data) bool {
	prof := zenProfile()
	dst := filepath.Join(d.Src(), ".zen")
	if prof == "" || !Exists(dst) {
		return false
	}
	changed := false
	for _, f := range zenFiles {
		if b, err := os.ReadFile(filepath.Join(prof, f)); err == nil {
			w, _ := WriteIfChanged(filepath.Join(dst, f), string(b))
			changed = changed || w
		}
	}
	if Exists(filepath.Join(prof, "chrome")) {
		before := Out("sh", "-c", "cd "+shq(dst)+" && find chrome -type f -exec shasum {} + | sort")
		_ = os.RemoveAll(filepath.Join(dst, "chrome"))
		_ = Run("rsync", "-a", "--exclude", ".DS_Store", filepath.Join(prof, "chrome")+"/", filepath.Join(dst, "chrome")+"/")
		after := Out("sh", "-c", "cd "+shq(dst)+" && find chrome -type f -exec shasum {} + | sort")
		changed = changed || before != after
	}
	if b, err := os.ReadFile(filepath.Join(prof, "extensions.json")); err == nil {
		var ext struct {
			Addons []struct {
				ID            string `json:"id"`
				Location      string `json:"location"`
				DefaultLocale struct {
					Name string `json:"name"`
				} `json:"defaultLocale"`
			} `json:"addons"`
		}
		if json.Unmarshal(b, &ext) == nil {
			var rows []string
			for _, a := range ext.Addons {
				if a.Location == "app-profile" {
					rows = append(rows, a.ID+"\t"+a.DefaultLocale.Name)
				}
			}
			sort.Strings(rows)
			w, _ := WriteIfChanged(filepath.Join(dst, "extensions.tsv"), strings.Join(rows, "\n")+"\n")
			changed = changed || w
		}
	}
	return changed
}

// CmdZenPush copies the whole Zen profile directory (sessions, logins) to host over ssh.
func CmdZenPush(host string) error {
	local := zenRoot()
	if zenRunning() {
		return fmt.Errorf("quit Zen here first (a running profile can't be copied safely)")
	}
	if OK("ssh", host, "pgrep -x zen") {
		return fmt.Errorf("quit Zen on %s first", host)
	}
	remote := ".zen"
	if Out("ssh", host, "uname") == "Darwin" {
		remote = "Library/Application Support/zen"
	}
	size := strings.Fields(Out("du", "-sh", local) + " ?")[0]
	if !confirm(fmt.Sprintf("Replace Zen on %s:~/%s with this machine's (%s)?", host, remote, size)) {
		return nil
	}
	if err := Run("ssh", host, "mkdir -p "+shq(remote)); err != nil {
		return err
	}
	err := Cmd{Args: []string{"rsync", "-a", "--delete", "--info=progress2", "--protect-args",
		"--exclude", "Crash Reports", "--exclude", "Pending Pings", local + "/", host + ":" + remote + "/"}}.Stream()
	if err == nil {
		Say("Zen profile copied to %s", host)
	}
	return err
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
