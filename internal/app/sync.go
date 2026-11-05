package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func jj(d *Data, args ...string) (string, error) {
	return Cmd{Args: append([]string{"jj"}, args...), Dir: d.Repo()}.Output()
}

// ---------------------------------------------------------------------------- capture

// captureFiles re-adds managed files edited in place since chezmoi last wrote them
// (first `chezmoi status` column M), so unapplied edits made in the repo are never overwritten.
func captureFiles(d *Data) []string {
	var paths []string
	for _, line := range strings.Split(Out("chezmoi", "status"), "\n") {
		if len(line) > 3 && line[0] == 'M' {
			paths = append(paths, filepath.Join(Home, line[3:]))
		}
	}
	if len(paths) == 0 {
		return nil
	}
	before := set(strings.Split(must(jj(d, "diff", "--name-only")), "\n"))
	_ = Run(append([]string{"chezmoi", "re-add"}, paths...)...)
	var changed []string
	for _, p := range strings.Split(must(jj(d, "diff", "--name-only")), "\n") {
		if p != "" && !before[p] {
			changed = append(changed, p)
		}
	}
	return changed
}

// captureCodex keeps the repo's copy of Codex's top-level settings in sync (its [tables] are machine state).
func captureCodex(d *Data) bool {
	tmpl := filepath.Join(d.Src(), ".chezmoitemplates/codex-settings.toml")
	b, err := os.ReadFile(filepath.Join(Home, ".codex/config.toml"))
	if err != nil || !Exists(tmpl) {
		return false
	}
	var top []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "[") {
			break
		}
		top = append(top, line)
	}
	text := strings.TrimRight(strings.Join(top, "\n"), "\n") + "\n"
	text = strings.ReplaceAll(text, Home, "{{ .chezmoi.homeDir }}")
	w, _ := WriteIfChanged(tmpl, text)
	return w
}

// newConfigDirs reports ~/.config entries that appeared since the last sync and aren't managed.
func newConfigDirs() []string {
	statePath := filepath.Join(StateDir, "config-dirs.json")
	entries, _ := os.ReadDir(filepath.Join(Home, ".config"))
	now := []string{}
	for _, e := range entries {
		now = append(now, e.Name())
	}
	var prev []string
	first := readJSON(statePath, &prev) != nil
	_ = writeJSON(statePath, now)
	if first {
		return nil
	}
	managed := Out("chezmoi", "managed", "--path-style", "relative")
	was := set(prev)
	var out []string
	for _, n := range now {
		if !was[n] && !strings.Contains(managed, ".config/"+n) {
			out = append(out, "~/.config/"+n)
		}
	}
	if len(out) > 0 {
		var pending []string
		_ = readJSON(filepath.Join(StateDir, "config-candidates.json"), &pending)
		_ = writeJSON(filepath.Join(StateDir, "config-candidates.json"), append(pending, out...))
	}
	return out
}

// CmdCapture pulls live changes into the repo and returns a summary for the commit message.
func CmdCapture(d *Data) []string {
	var parts []string
	if files := captureFiles(d); len(files) > 0 {
		names := map[string]bool{}
		for _, f := range files {
			n := filepath.Base(f)
			for _, p := range []string{"private_", "dot_", "executable_", "readonly_"} {
				n = strings.ReplaceAll(n, p, map[string]string{"dot_": "."}[p])
			}
			names[n] = true
		}
		list := keys(names)
		if len(list) > 6 {
			list = append(list[:6], fmt.Sprintf("+%d", len(list)-6))
		}
		parts = append(parts, strings.Join(list, ", "))
	}
	if captureCodex(d) {
		parts = append(parts, "codex settings")
	}
	if ch := captureTrackedPrefs(d); len(ch) > 0 {
		if len(ch) > 4 {
			ch = append(ch[:4], "…")
		}
		parts = append(parts, "prefs ("+strings.Join(ch, ", ")+")")
	}
	if n := capturePrefsInbox(d); n > 0 {
		Say("%d new setting change(s) to review: `dotfiles prefs`", n)
	}
	if captureZen(d) {
		parts = append(parts, "zen")
	}
	if notes := capturePackages(d); len(notes) > 0 {
		if len(notes) > 8 {
			notes = append(notes[:8], "…")
		}
		parts = append(parts, "packages "+strings.Join(notes, " "))
	}
	if dirs := newConfigDirs(); len(dirs) > 0 {
		Say("new config folders (track with `dotfiles add`): %s", strings.Join(dirs, ", "))
	}
	if sec := captureSecrets(d); len(sec) > 0 {
		Notify("dotfiles", "secrets changed ("+strings.Join(sec, ", ")+"); run `dotfiles secrets push`")
	}
	for _, p := range parts {
		Say("captured %s", p)
	}
	return parts
}

// ---------------------------------------------------------------------------- leak guard

func hasRemote(d *Data) bool {
	return strings.Contains(must(jj(d, "git", "remote", "list")), "origin")
}

func remoteMainExists(d *Data) bool {
	_, err := jj(d, "log", "-r", "main@origin", "--no-graph", "-T", "''")
	return err == nil
}

// CmdGuard scans commits not yet on origin (or rev) for secrets. Returns false if something was found.
func CmdGuard(d *Data, rev string) bool {
	from := "root()"
	if rev == "" && remoteMainExists(d) {
		from = "main@origin"
	}
	var diff string
	if rev != "" {
		diff = must(jj(d, "diff", "-r", rev, "--git"))
	} else {
		diff = must(jj(d, "diff", "--from", from, "--to", "@", "--git"))
	}
	var added strings.Builder
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			added.WriteString(l[1:] + "\n")
		}
	}
	text := added.String()
	if strings.TrimSpace(text) == "" {
		Say("guard: nothing outgoing")
		return true
	}
	var hits []string
	for _, n := range secretNeedles() {
		if strings.Contains(text, n) {
			hits = append(hits, n[:4]+"…")
		}
	}
	if Has("gitleaks") {
		tmp, _ := os.MkdirTemp("", "dotfiles-guard")
		defer os.RemoveAll(tmp)
		_ = os.WriteFile(filepath.Join(tmp, "added.txt"), []byte(text), 0o600)
		args := []string{"gitleaks", "dir", tmp, "--no-banner", "--redact", "--report-format", "json", "--report-path", filepath.Join(tmp, "r.json")}
		if cfg := filepath.Join(d.Repo(), ".gitleaks.toml"); Exists(cfg) {
			args = append(args, "--config", cfg)
		}
		if exec.Command(args[0], args[1:]...).Run() != nil {
			var rep []struct {
				RuleID    string
				StartLine int
			}
			b, _ := os.ReadFile(filepath.Join(tmp, "r.json"))
			_ = json.Unmarshal(b, &rep)
			for _, r := range rep {
				hits = append(hits, fmt.Sprintf("%s (line %d)", r.RuleID, r.StartLine))
			}
		}
	} else {
		Warn("gitleaks not installed; only checking known secret values")
	}
	if len(hits) > 0 {
		Notify("dotfiles push blocked", "possible secrets in outgoing changes: "+strings.Join(hits, ", "))
		return false
	}
	Say("guard: clean")
	return true
}

// ---------------------------------------------------------------------------- sync

// CmdSync: capture -> commit -> fetch/rebase -> guard -> push -> apply.
func CmdSync() error {
	d, err := LoadData()
	if err != nil {
		return err
	}
	parts := CmdCapture(d)
	if strings.TrimSpace(must(jj(d, "diff", "--name-only"))) != "" {
		msg := "auto: update"
		if len(parts) > 0 {
			msg = "auto: " + strings.Join(parts, "; ")
		}
		msg += " (" + Hostname() + ")"
		if _, err := jj(d, "describe", "-m", msg); err != nil {
			return err
		}
		if _, err := jj(d, "new"); err != nil {
			return err
		}
		Say("committed: %s", msg)
	}
	if !hasRemote(d) {
		Say("no remote; commits stay local")
	} else {
		if _, err := jj(d, "git", "fetch"); err != nil {
			Warn("fetch failed: %v", firstLine(err.Error()))
		} else if remoteMainExists(d) {
			if _, err := jj(d, "rebase", "-b", "@", "-d", "main@origin"); err != nil {
				return err
			}
		}
		if c := must(jj(d, "log", "-r", "conflicts()", "--no-graph", "-T", "change_id.short() ++ ' '")); c != "" {
			Notify("dotfiles sync stopped", "merge conflict after rebase in "+d.Repo())
			return nil
		}
		outgoing := "::@-"
		if remoteMainExists(d) {
			outgoing = "main@origin..@-"
		}
		if must(jj(d, "log", "-r", outgoing, "--no-graph", "-T", "'x'")) != "" {
			if !CmdGuard(d, "") {
				return nil
			}
			_, _ = jj(d, "bookmark", "set", "main", "-r", "@-", "--allow-backwards")
			if _, err := jj(d, "git", "push", "--bookmark", "main"); err != nil {
				if _, err2 := jj(d, "git", "push", "--bookmark", "main", "--allow-new"); err2 != nil {
					return err
				}
			}
			Say("pushed")
		}
	}
	Say("chezmoi apply")
	return Cmd{Args: []string{"chezmoi", "apply", "--force", "--no-tty"}}.Stream()
}

// ---------------------------------------------------------------------------- status

// CmdStatus shows drift and things waiting for you.
func CmdStatus() error {
	d, err := LoadData()
	if err != nil {
		return err
	}
	fmt.Printf("repo:     %s\nprofile:  %s   groups: %s   auto-sync: %v\n", d.Repo(), d.Profile, strings.Join(d.EnabledGroups(), ", "), d.Autosync)
	if len(d.Exclude) > 0 {
		fmt.Printf("excluded: %s\n", strings.Join(d.Exclude, ", "))
	}
	section := func(title, body string) {
		if strings.TrimSpace(body) == "" {
			body = "  none"
		}
		fmt.Printf("\n%s\n%s\n", stepStyle.Render(title), body)
	}
	section("Live files that differ from the repo (chezmoi status)", indent(Out("chezmoi", "status")))
	section("Uncommitted repo changes", indent(must(jj(d, "diff", "--stat"))))
	inbox := d.Packages.Groups["inbox"]
	items := append(append(append([]string{}, inbox.Brew...), inbox.Cask...), inbox.Mise...)
	for _, m := range inbox.Mas {
		items = append(items, m.Name)
	}
	sort.Strings(items)
	section("Package inbox (sort into packages.toml)", indent(strings.Join(items, ", ")))
	var prefs []PrefChange
	_ = readJSON(prefsInboxPath, &prefs)
	if len(prefs) > 0 {
		section("Settings to review", fmt.Sprintf("  %d changed setting(s): run `dotfiles prefs`", len(prefs)))
	}
	var cands []string
	_ = readJSON(filepath.Join(StateDir, "config-candidates.json"), &cands)
	if len(cands) > 0 {
		section("New config folders (track with `dotfiles add <path>`)", indent(strings.Join(cands, "\n")))
	}
	if b, err := os.ReadFile(filepath.Join(StateDir, "secrets-dirty")); err == nil {
		section("Secrets changed locally (run `dotfiles secrets push`)", indent(strings.TrimSpace(string(b))))
	}
	if n := loginPendingCount(d); n > 0 {
		section("Accounts", fmt.Sprintf("  %d app(s) not signed in: run `dotfiles login`", n))
	}
	if IsMac {
		section("Apps not managed (`dotfiles exclude NAME` to stop listing one)", indent(strings.Join(unmanagedApps(d.Exclude), "\n")))
	}
	return nil
}

func indent(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// CmdAdd starts tracking a path and clears it from the candidates list.
func CmdAdd(paths []string) error {
	if len(paths) > 0 && paths[0] == "--secret" {
		return CmdAddSecret(paths[1:])
	}
	if len(paths) == 0 {
		return fmt.Errorf("usage: dotfiles add [--secret] PATH...")
	}
	args := []string{"chezmoi", "add"}
	for _, p := range paths {
		args = append(args, ExpandHome(p))
	}
	if err := (Cmd{Args: args}).Stream(); err != nil {
		return err
	}
	var cands, keep []string
	_ = readJSON(filepath.Join(StateDir, "config-candidates.json"), &cands)
	added := set(paths)
	for _, c := range cands {
		if !added[c] && !added[ExpandHome(c)] {
			keep = append(keep, c)
		}
	}
	return writeJSON(filepath.Join(StateDir, "config-candidates.json"), keep)
}
