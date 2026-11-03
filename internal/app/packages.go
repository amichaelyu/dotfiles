package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"howett.net/plist"
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
		if !known["brew"][name] && !d.Excluded(name) {
			add(&inbox.Brew, name)
		}
	}
	for name := range got.Cask {
		if !known["cask"][name] && !d.Excluded(name) {
			add(&inbox.Cask, name)
		}
	}
	for name := range got.Mise {
		if !known["mise"][name] && !d.Excluded(name) {
			add(&inbox.Mise, name)
		}
	}
	for id, name := range got.Mas {
		if !knownMas[id] && !d.Excluded(name) {
			inbox.Mas = append(inbox.Mas, MasApp{Name: name, ID: id})
			notes = append(notes, "+"+name)
		}
	}
	if len(notes) > 0 {
		if _, err := writeInbox(d.Src(), inbox); err != nil {
			Warn("inbox: %v", err)
		}
	}

	// Uninstall detection: wanted here, installed at the last sync, gone now. This only affects
	// THIS machine: the package joins its local exclude list so it isn't reinstalled here.
	// Removing a package from every machine is explicit: `dotfiles remove PKG --everywhere`.
	statePath := filepath.Join(StateDir, "packages.json")
	var prev pkgState
	hadPrev := readJSON(statePath, &prev) == nil
	if hadPrev {
		was := map[string]map[string]bool{"brew": set(prev.Brew), "cask": set(prev.Cask), "mise": set(prev.Mise)}
		var gone []string
		for _, gname := range d.EnabledGroups() {
			g := d.Packages.Groups[gname]
			for kind, list := range map[string][]string{"brew": g.Brew, "cask": g.Cask, "mise": g.Mise} {
				for _, name := range list {
					if (was[kind][name] || was[kind][short(name)]) && !got.has(kind, name) && !d.Excluded(name) {
						gone = append(gone, name)
					}
				}
			}
		}
		if len(gone) > 0 {
			mc := ReadMachineConfig()
			mc.Exclude = append(mc.Exclude, gone...)
			if err := WriteMachineConfig(mc); err == nil {
				Say("uninstalled here, so excluded on this machine only: %s (other machines keep it; `dotfiles remove PKG --everywhere` to drop it everywhere)", strings.Join(gone, ", "))
			}
		}
	}
	prev.Brew, prev.Cask, prev.Mise = sortedKeys(got.Brew), sortedKeys(got.Cask), sortedKeys(got.Mise)
	_ = writeJSON(statePath, prev)
	return notes
}

// CmdRemove uninstalls packages here and excludes them on this machine; with --everywhere it also
// lists them under [packages.removed] so every machine uninstalls them on its next apply.
func CmdRemove(args []string) error {
	everywhere := false
	var names []string
	for _, a := range args {
		if a == "--everywhere" {
			everywhere = true
		} else {
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("usage: dotfiles remove PKG... [--everywhere]")
	}
	d, err := LoadData()
	if err != nil {
		return err
	}
	got := snapshotInstalled()
	mc := ReadMachineConfig()
	for _, n := range names {
		kind := "brew"
		switch {
		case got.has("cask", n) || inAnyGroup(d, "cask", n):
			kind = "cask"
		case !IsMac:
			kind = "mise"
		}
		if got.has(kind, n) {
			Say("uninstalling %s", n)
			switch kind {
			case "cask":
				_ = Cmd{Args: []string{"brew", "uninstall", "--cask", n}}.Stream()
			case "brew":
				_ = Cmd{Args: []string{"brew", "uninstall", n}}.Stream()
			case "mise":
				_ = Run(MiseBin, "uninstall", "--all", n)
			}
		}
		if everywhere {
			if err := moveToRemoved(d.Src(), kind, n); err != nil {
				return err
			}
			Say("%s listed under [packages.removed]; other machines uninstall it on their next sync", n)
		} else if !contains(mc.Exclude, n) {
			mc.Exclude = append(mc.Exclude, n)
		}
	}
	if !everywhere {
		Say("excluded on this machine only")
	}
	return WriteMachineConfig(mc)
}

func inAnyGroup(d *Data, kind, name string) bool {
	for _, g := range groupsOf(d) {
		list := map[string][]string{"brew": g.Brew, "cask": g.Cask, "mise": g.Mise}[kind]
		for _, n := range list {
			if n == name || short(n) == name {
				return true
			}
		}
	}
	return false
}

func groupsOf(d *Data) []Group {
	out := []Group{}
	for _, n := range d.AllGroupNames() {
		out = append(out, d.Packages.Groups[n])
	}
	return out
}

// caskOwnedApps returns /Applications-relative paths (e.g. "Zed.app", "KiCad/KiCad.app") that installed
// casks account for: their app artifacts, suite folders, and every app inside the .pkg receipts they
// declare (Office, Zoom, Tailscale, ... install through a .pkg and never name the app).
func caskOwnedApps() (apps, suites, bundleIDs map[string]bool) {
	apps, suites, bundleIDs = map[string]bool{}, map[string]bool{}, map[string]bool{}
	var info struct {
		Casks []struct {
			Artifacts []map[string]json.RawMessage `json:"artifacts"`
		} `json:"casks"`
	}
	if json.Unmarshal([]byte(Out("brew", "info", "--cask", "--installed", "--json=v2")), &info) != nil {
		return
	}
	var pkgPatterns []string
	for _, c := range info.Casks {
		for _, a := range c.Artifacts {
			for kind, raw := range a {
				var list []any
				_ = json.Unmarshal(raw, &list)
				switch kind {
				case "app", "suite":
					for _, x := range list {
						name := ""
						switch v := x.(type) {
						case string:
							name = v
						case map[string]any:
							name, _ = v["target"].(string)
						}
						if name == "" {
							continue
						}
						if kind == "suite" {
							suites[filepath.Base(name)] = true
						} else {
							apps[strings.TrimPrefix(name, "/Applications/")] = true
							apps[filepath.Base(name)] = true
						}
					}
				case "uninstall":
					for _, x := range list {
						m, ok := x.(map[string]any)
						if !ok {
							continue
						}
						pkgPatterns = append(pkgPatterns, stringsOf(m["pkgutil"])...)
						// installer-script casks only name the app via delete paths or bundle IDs to quit
						for _, p := range stringsOf(m["delete"]) {
							if rel, ok := strings.CutPrefix(p, "/Applications/"); ok {
								apps[rel] = true
							}
						}
						for _, id := range stringsOf(m["quit"]) {
							bundleIDs[id] = true
						}
					}
				}
			}
		}
	}
	if len(pkgPatterns) == 0 {
		return
	}
	var res []*regexp.Regexp
	for _, p := range pkgPatterns {
		if re, err := regexp.Compile("^(" + p + ")$"); err == nil {
			res = append(res, re)
		}
	}
	for _, id := range strings.Split(Out("pkgutil", "--pkgs"), "\n") {
		matched := false
		for _, re := range res {
			if re.MatchString(id) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		loc := ""
		for _, line := range strings.Split(Out("pkgutil", "--pkg-info", id), "\n") {
			if strings.HasPrefix(line, "location: ") {
				loc = strings.TrimPrefix(line, "location: ")
			}
		}
		for _, f := range strings.Split(Out("pkgutil", "--files", id), "\n") {
			full := strings.TrimPrefix(filepath.Join("/", loc, f), "/")
			rel, ok := strings.CutPrefix(full, "Applications/")
			if !ok || !strings.HasSuffix(rel, ".app") || strings.Contains(rel, ".app/") {
				continue
			}
			apps[rel] = true
		}
	}
	return
}

// unmanagedApps lists apps in /Applications (and one folder deep) that no installed cask or the
// App Store accounts for, minus this machine's exclude list.
func unmanagedApps(exclude []string) []string {
	owned, suites, bundleIDs := caskOwnedApps()
	var apps []string
	entries, _ := os.ReadDir("/Applications")
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".app") {
			apps = append(apps, n)
		} else if e.IsDir() && n != "Utilities" && !suites[n] {
			sub, _ := os.ReadDir(filepath.Join("/Applications", n))
			for _, s := range sub {
				if strings.HasSuffix(s.Name(), ".app") {
					apps = append(apps, n+"/"+s.Name())
				}
			}
		}
	}
	var out []string
	for _, rel := range apps {
		if strings.HasPrefix(rel, "Safari") || owned[rel] || Exists(filepath.Join("/Applications", rel, "Contents/_MASReceipt")) ||
			bundleIDs[bundleID(filepath.Join("/Applications", rel))] || matchesAny(exclude, rel) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// autoAdopt turns drag-installed apps into casks when a cask installs exactly that app.
func autoAdopt(d *Data) {
	statePath := filepath.Join(StateDir, "packages.json")
	var st pkgState
	_ = readJSON(statePath, &st)
	tried := set(st.AdoptTried)
	for _, app := range unmanagedApps(d.Exclude) {
		if strings.Contains(app, "/") {
			continue // apps inside folders come from installers, not drag-and-drop
		}
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

// CmdExclude manages this machine's exclude list (kept in ~/.config/chezmoi/chezmoi.toml, not the repo).
// Excluded packages/apps aren't installed, captured, adopted, flagged or treated as uninstalled here.
//
//	dotfiles exclude                list
//	dotfiles exclude NAME...        add (wildcards ok: "Microsoft *")
//	dotfiles exclude --rm NAME...   remove
func CmdExclude(args []string) error {
	mc := ReadMachineConfig()
	if len(args) == 0 {
		if len(mc.Exclude) == 0 {
			Say("nothing excluded on this machine")
		}
		for _, e := range mc.Exclude {
			fmt.Println("  " + e)
		}
		return nil
	}
	rm := args[0] == "--rm"
	if rm {
		args = args[1:]
	}
	for _, a := range args {
		a = strings.TrimSuffix(a, ".app")
		if rm {
			var keep []string
			for _, e := range mc.Exclude {
				if e != a {
					keep = append(keep, e)
				}
			}
			mc.Exclude = keep
		} else if !contains(mc.Exclude, a) {
			mc.Exclude = append(mc.Exclude, a)
		}
	}
	sort.Strings(mc.Exclude)
	if err := WriteMachineConfig(mc); err != nil {
		return err
	}
	if rm {
		Say("no longer excluded: %s", strings.Join(args, ", "))
	} else {
		Say("excluded on this machine: %s", strings.Join(args, ", "))
	}
	return nil
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, y := range x {
			if s, ok := y.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// bundleID reads CFBundleIdentifier from an app bundle ("" if unreadable).
func bundleID(app string) string {
	b, err := os.ReadFile(filepath.Join(app, "Contents/Info.plist"))
	if err != nil {
		return ""
	}
	var info struct {
		ID string `plist:"CFBundleIdentifier"`
	}
	if _, err := plist.Unmarshal(b, &info); err != nil {
		return ""
	}
	return info.ID
}
