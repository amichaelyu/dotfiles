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

// ---------------------------------------------------------------------------- install (hook)

// Brewfile renders the Brewfile for this machine's enabled groups minus excludes.
func (d *Data) Brewfile() string {
	var b strings.Builder
	for _, t := range d.Packages.TrustedTaps.Taps {
		fmt.Fprintf(&b, "tap %q, trusted: true\n", t)
	}
	for _, name := range d.EnabledGroups() {
		g := d.Packages.Groups[name]
		fmt.Fprintf(&b, "# --- %s: %s\n", name, g.Description)
		for _, p := range g.Brew {
			if !d.Excluded(p) {
				fmt.Fprintf(&b, "brew %q\n", p)
			}
		}
		for _, p := range g.Cask {
			if !d.Excluded(p) {
				fmt.Fprintf(&b, "cask %q\n", p)
			}
		}
		for _, m := range g.Mas {
			if !d.Excluded(m.Name) {
				fmt.Fprintf(&b, "mas %q, id: %d\n", m.Name, m.ID)
			}
		}
	}
	return b.String()
}

// HookPackages installs the packages for this machine (brew/mas on macOS, mise on Linux).
func HookPackages(d *Data) error {
	if IsMac {
		return installMac(d)
	}
	return installLinux(d)
}

func ensureBrew() error {
	if Has("brew") {
		return nil
	}
	for _, p := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if Exists(filepath.Join(p, "brew")) {
			os.Setenv("PATH", p+":"+os.Getenv("PATH"))
			return nil
		}
	}
	Say("installing Homebrew")
	err := Cmd{Args: []string{"/bin/bash", "-c", `$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)`}}.Stream()
	os.Setenv("PATH", "/opt/homebrew/bin:/opt/homebrew/sbin:"+os.Getenv("PATH"))
	return err
}

func installMac(d *Data) error {
	if err := ensureBrew(); err != nil {
		return err
	}
	Say("brew bundle (%s)", strings.Join(d.EnabledGroups(), ", "))
	err := Cmd{Args: []string{"brew", "bundle", "--file=-", "--no-upgrade"}, Stdin: d.Brewfile()}.Stream()
	if err != nil {
		Warn("some packages failed (App Store apps need you signed in to the App Store); re-run `chezmoi apply` later")
	}
	for _, c := range d.Packages.Removed.Cask {
		if OK("brew", "list", "--cask", c) {
			Say("removing cask %s", c)
			_ = Cmd{Args: []string{"brew", "uninstall", "--cask", c}}.Stream()
		}
	}
	for _, f := range d.Packages.Removed.Brew {
		if OK("brew", "list", "--formula", f) {
			Say("removing %s", f)
			_ = Cmd{Args: []string{"brew", "uninstall", f}}.Stream()
		}
	}
	return nil
}

// MiseBin is where the Linux installer puts mise.
var MiseBin = filepath.Join(Home, ".local/bin/mise")

func installLinux(d *Data) error {
	if Has("apt-get") && OK("sudo", "-n", "true") {
		Say("apt base packages")
		_ = Cmd{Args: []string{"sh", "-c", "sudo apt-get update -qq && sudo apt-get install -y -qq zsh git curl unzip build-essential"}}.Stream()
	}
	if !Exists(MiseBin) {
		Say("installing mise")
		if err := (Cmd{Args: []string{"sh", "-c", "curl -fsSL https://mise.run | sh"}}).Stream(); err != nil {
			return err
		}
	}
	var b strings.Builder
	b.WriteString("# Written by `dotfiles hook packages` from packages.toml. Edit packages.toml instead.\n[tools]\n")
	for _, name := range d.EnabledGroups() {
		for _, t := range d.Packages.Groups[name].Mise {
			if !d.Excluded(t) {
				fmt.Fprintf(&b, "%q = \"latest\"\n", t)
			}
		}
	}
	if _, err := WriteIfChanged(filepath.Join(Home, ".config/mise/conf.d/dotfiles.toml"), b.String()); err != nil {
		return err
	}
	Say("mise install")
	_ = Cmd{Args: []string{MiseBin, "install", "--yes"}}.Stream()
	for _, t := range d.Packages.Removed.Mise {
		_ = Run(MiseBin, "uninstall", "--all", t)
	}
	return nil
}

// ---------------------------------------------------------------------------- what's installed

type installed struct {
	Brew, Cask, Mise map[string]bool
	Mas              map[int64]string
}

func snapshotInstalled() installed {
	got := installed{Brew: map[string]bool{}, Cask: map[string]bool{}, Mise: map[string]bool{}, Mas: map[int64]string{}}
	if IsMac && Has("brew") {
		re := regexp.MustCompile(`^(brew|cask) "([^"]+)"`)
		for _, line := range strings.Split(Out("brew", "bundle", "dump", "--file=-", "--formula", "--cask"), "\n") {
			if m := re.FindStringSubmatch(line); m != nil {
				if m[1] == "brew" {
					got.Brew[m[2]] = true
				} else {
					got.Cask[m[2]] = true
				}
			}
		}
		if Has("mas") {
			re := regexp.MustCompile(`^\s*(\d+)\s+(.+?)\s+\(`)
			for _, line := range strings.Split(Out("mas", "list"), "\n") {
				if m := re.FindStringSubmatch(line); m != nil {
					var id int64
					fmt.Sscan(m[1], &id)
					got.Mas[id] = m[2]
				}
			}
		}
	} else if Exists(MiseBin) || Has("mise") {
		bin := MiseBin
		if !Exists(bin) {
			bin = "mise"
		}
		var tools map[string][]any
		if json.Unmarshal([]byte(Out(bin, "ls", "--installed", "--json")), &tools) == nil {
			for t, v := range tools {
				if len(v) > 0 {
					got.Mise[short(strings.TrimPrefix(t, "aqua:"))] = true
				}
			}
		}
	}
	return got
}

func (i installed) has(kind, name string) bool {
	m := map[string]map[string]bool{"brew": i.Brew, "cask": i.Cask, "mise": i.Mise}[kind]
	return m[name] || m[short(name)]
}

// ---------------------------------------------------------------------------- capture

type pkgState struct {
	Brew, Cask, Mise []string
	AdoptTried       []string
}

// capturePackages records new installs in inbox.toml and propagates uninstalls to [packages.removed].
func capturePackages(d *Data) []string {
	if IsMac {
		autoAdopt(d)
	}
	got := snapshotInstalled()
	known := map[string]map[string]bool{"brew": {}, "cask": {}, "mise": {}}
	knownMas := map[int64]bool{}
	for _, g := range append(groupsOf(d), d.Packages.Removed) {
		for kind, list := range map[string][]string{"brew": g.Brew, "cask": g.Cask, "mise": g.Mise} {
			for _, n := range list {
				known[kind][n], known[kind][short(n)] = true, true
			}
		}
		for _, m := range g.Mas {
			knownMas[m.ID] = true
		}
	}
	inbox := d.Packages.Groups["inbox"]
	var notes []string
	add := func(list *[]string, name string) { *list = append(*list, name); notes = append(notes, "+"+name) }
	for name := range got.Brew {
		if !known["brew"][name] {
			add(&inbox.Brew, name)
		}
	}
	for name := range got.Cask {
		if !known["cask"][name] {
			add(&inbox.Cask, name)
		}
	}
	for name := range got.Mise {
		if !known["mise"][name] {
			add(&inbox.Mise, name)
		}
	}
	for id, name := range got.Mas {
		if !knownMas[id] {
			inbox.Mas = append(inbox.Mas, MasApp{Name: name, ID: id})
			notes = append(notes, "+"+name)
		}
	}
	if len(notes) > 0 {
		if _, err := writeInbox(d.Src(), inbox); err != nil {
			Warn("inbox: %v", err)
		}
	}

	// Uninstall detection: wanted here, installed at the last sync, gone now.
	statePath := filepath.Join(StateDir, "packages.json")
	var prev pkgState
	hadPrev := readJSON(statePath, &prev) == nil
	if hadPrev {
		was := map[string]map[string]bool{"brew": set(prev.Brew), "cask": set(prev.Cask), "mise": set(prev.Mise)}
		for _, gname := range d.EnabledGroups() {
			g := d.Packages.Groups[gname]
			for kind, list := range map[string][]string{"brew": g.Brew, "cask": g.Cask, "mise": g.Mise} {
				for _, name := range list {
					if (was[kind][name] || was[kind][short(name)]) && !got.has(kind, name) && !d.Excluded(name) {
						if err := moveToRemoved(d.Src(), kind, name); err == nil {
							notes = append(notes, "-"+short(name))
						}
					}
				}
			}
		}
	}
	prev.Brew, prev.Cask, prev.Mise = sortedKeys(got.Brew), sortedKeys(got.Cask), sortedKeys(got.Mise)
	_ = writeJSON(statePath, prev)
	return notes
}

func groupsOf(d *Data) []Group {
	out := []Group{}
	for _, n := range d.AllGroupNames() {
		out = append(out, d.Packages.Groups[n])
	}
	return out
}

// unmanagedApps lists /Applications apps not from a brew cask or the App Store.
func unmanagedApps() []string {
	caskApps := map[string]bool{}
	var info struct {
		Casks []struct {
			Artifacts []any `json:"artifacts"`
		} `json:"casks"`
	}
	if json.Unmarshal([]byte(Out("brew", "info", "--cask", "--installed", "--json=v2")), &info) == nil {
		for _, c := range info.Casks {
			for _, a := range c.Artifacts {
				if m, ok := a.(map[string]any); ok {
					if apps, ok := m["app"].([]any); ok {
						for _, x := range apps {
							switch v := x.(type) {
							case string:
								caskApps[filepath.Base(v)] = true
							case map[string]any:
								if t, ok := v["target"].(string); ok {
									caskApps[filepath.Base(t)] = true
								}
							}
						}
					}
				}
			}
		}
	}
	entries, _ := os.ReadDir("/Applications")
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".app") || caskApps[n] || Exists(filepath.Join("/Applications", n, "Contents/_MASReceipt")) {
			continue
		}
		if strings.HasPrefix(n, "Safari") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// autoAdopt turns drag-installed apps into casks when a cask installs exactly that app.
func autoAdopt(d *Data) {
	statePath := filepath.Join(StateDir, "packages.json")
	var st pkgState
	_ = readJSON(statePath, &st)
	tried := set(st.AdoptTried)
	for _, app := range unmanagedApps() {
		if tried[app] {
			continue
		}
		st.AdoptTried = append(st.AdoptTried, app)
		token := strings.ToLower(strings.NewReplacer(" ", "-", ".app", "").Replace(app))
		js := Out("brew", "info", "--cask", "--json=v2", token)
		if !strings.Contains(js, q(app)) && !strings.Contains(js, q("/Applications/"+app)) {
			continue
		}
		Say("adopting %s as cask %s", app, token)
		if err := Run("brew", "install", "--cask", "--adopt", token); err != nil {
			Warn("adopt %s failed: %v", token, firstLine(err.Error()))
		}
	}
	_ = writeJSON(statePath, st)
}

// ---------------------------------------------------------------------------- small helpers

func set(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[s] = true
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := keys(m)
	sort.Strings(out)
	return out
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(path, b, 0o644)
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }
