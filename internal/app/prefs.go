package app

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"howett.net/plist"
)

// macosData is macos.toml read with exact TOML types (int64 vs float64 matter for defaults).
type macosData struct {
	Macos struct {
		Prefs           map[string]map[string]any `toml:"prefs"`
		Symbolichotkeys struct {
			Disabled []int `toml:"disabled"`
		} `toml:"symbolichotkeys"`
	} `toml:"macos"`
}

func loadMacos(src string) (*macosData, error) {
	var m macosData
	_, err := toml.DecodeFile(filepath.Join(src, macosFile), &m)
	if m.Macos.Prefs == nil {
		m.Macos.Prefs = map[string]map[string]any{}
	}
	return &m, err
}

func splitDomain(dom string) (flags []string, name string) {
	if strings.HasPrefix(dom, "currentHost:") {
		return []string{"-currentHost"}, strings.TrimPrefix(dom, "currentHost:")
	}
	return nil, dom
}

// readDomain returns a defaults domain as a map.
func readDomain(dom string) map[string]any {
	flags, name := splitDomain(dom)
	out, err := exec.Command("defaults", append(flags, "export", name, "-")...).Output()
	if err != nil || len(out) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if _, err := plist.Unmarshal(out, &m); err != nil {
		return map[string]any{}
	}
	return m
}

func isScalar(v any) bool {
	switch v.(type) {
	case bool, int64, uint64, float64, string:
		return true
	}
	return false
}

func normalize(v any) any {
	if u, ok := v.(uint64); ok {
		return int64(u)
	}
	return v
}

func typedArgs(v any) []string {
	switch x := v.(type) {
	case bool:
		return []string{"-bool", fmt.Sprint(x)}
	case int64:
		return []string{"-int", fmt.Sprint(x)}
	case uint64:
		return []string{"-int", fmt.Sprint(x)}
	case float64:
		return []string{"-float", fmt.Sprint(x)}
	}
	return []string{"-string", fmt.Sprint(v)}
}

// HookPrefs applies macos.toml with typed `defaults write`.
func HookPrefs(d *Data) error {
	if !IsMac {
		return nil
	}
	m, err := loadMacos(d.Src())
	if err != nil {
		return err
	}
	n := 0
	for _, dom := range keys(m.Macos.Prefs) {
		flags, name := splitDomain(dom)
		for _, k := range keys(m.Macos.Prefs[dom]) {
			args := append(append(append([]string{"defaults"}, flags...), "write", name, k), typedArgs(m.Macos.Prefs[dom][k])...)
			if err := Run(args...); err != nil {
				Warn("%v", firstLine(err.Error()))
			} else {
				n++
			}
		}
	}
	if len(m.Macos.Symbolichotkeys.Disabled) > 0 {
		disableHotkeys(m.Macos.Symbolichotkeys.Disabled)
	}
	_ = Run("/System/Library/PrivateFrameworks/SystemAdministration.framework/Resources/activateSettings", "-u")
	_ = Run("killall", "Dock", "Finder", "SystemUIServer", "ControlCenter")
	Say("macOS settings applied (%d keys; some need a logout)", n)
	return nil
}

// disableHotkeys flips `enabled` off for system shortcuts, keeping their key bindings.
func disableHotkeys(ids []int) {
	tmp := filepath.Join(os.TempDir(), "dotfiles-hotkeys.plist")
	defer os.Remove(tmp)
	if Run("defaults", "export", "com.apple.symbolichotkeys", tmp) != nil {
		return
	}
	for _, id := range ids {
		if Run("/usr/libexec/PlistBuddy", "-c", fmt.Sprintf("Set :AppleSymbolicHotKeys:%d:enabled false", id), tmp) != nil {
			_ = Run("/usr/libexec/PlistBuddy", "-c", fmt.Sprintf("Add :AppleSymbolicHotKeys:%d:enabled bool false", id), tmp)
		}
	}
	_ = Run("defaults", "import", "com.apple.symbolichotkeys", tmp)
}

// captureTrackedPrefs refreshes values of keys already in macos.toml.
func captureTrackedPrefs(d *Data) []string {
	if !IsMac {
		return nil
	}
	m, err := loadMacos(d.Src())
	if err != nil {
		return nil
	}
	var changed []string
	for dom, kv := range m.Macos.Prefs {
		live := readDomain(dom)
		for k, v := range kv {
			if lv, ok := live[k]; ok && isScalar(lv) && fmt.Sprint(normalize(lv)) != fmt.Sprint(v) {
				kv[k] = normalize(lv)
				changed = append(changed, short(strings.ReplaceAll(dom, ".", "/"))+"."+k)
			}
		}
	}
	if len(changed) > 0 {
		_, _ = writeMacos(d.Src(), m.Macos.Prefs, m.Macos.Symbolichotkeys.Disabled)
	}
	sort.Strings(changed)
	return changed
}

// ---------------------------------------------------------------------------- prefs inbox

// PrefChange is a candidate setting noticed by the daily snapshot.
type PrefChange struct {
	Domain string `json:"domain"`
	Key    string `json:"key"`
	Old    any    `json:"old"`
	New    any    `json:"new"`
	Seen   string `json:"seen"`
}

var (
	prefNoiseKey   = regexp.MustCompile(`(?i)(time|date|stamp|count|last|window|frame|uuid|recent|cache|version|seen|launch|session|token|id$|size$|position|history|state$|migrat|analytics|telemetry|nsstatusitem|nstoolbar|nssplitview|nsnavpanel|nstableview|selected|expanded|scroll|zoom)`)
	prefNoiseDom   = regexp.MustCompile(`(?i)(^com\.apple\.(spaces|knowledge|xpc|security|commerce|appstore|softwareupdate|SoftwareUpdate|bird|cloudd|CallHistory|coreservices|iCal|ncprefs|sharingd|lookup|identityservices|imessage|madrid|ids|photos|mail|news|stocks|tips|TV|Music|loginwindow|ServicesMenu|universalaccessAuthWarning|CoreGraphics)|^(ContextStoreAgent|com\.google\.Keystone|org\.sparkle|com\.segment|com\.launchdarkly|com\.telemetrydeck|com\.firebase|TokenBucket|SKGActivity))`)
	prefsSnapPath  = filepath.Join(StateDir, "prefs-snapshot.json")
	prefsInboxPath = filepath.Join(StateDir, "prefs-inbox.json")
	prefsIgnPath   = filepath.Join(StateDir, "prefs-ignored.json")
)

func allDomains() map[string]map[string]any {
	names := []string{"NSGlobalDomain"}
	for _, d := range strings.Split(Out("defaults", "domains"), ",") {
		if d = strings.TrimSpace(d); d != "" && !prefNoiseDom.MatchString(d) {
			names = append(names, d)
		}
	}
	names = append(names, "currentHost:com.apple.controlcenter")
	snap := map[string]map[string]any{}
	for _, dom := range names {
		vals := map[string]any{}
		for k, v := range readDomain(dom) {
			if isScalar(v) {
				vals[k] = normalize(v)
			}
		}
		snap[dom] = vals
	}
	return snap
}

func diffSnapshots(before, after map[string]map[string]any, tracked map[string]map[string]any) []PrefChange {
	var out []PrefChange
	now := time.Now().Format("2006-01-02")
	for dom, kv := range after {
		for k, v := range kv {
			old, had := before[dom][k]
			if had && fmt.Sprint(old) == fmt.Sprint(v) {
				continue
			}
			if _, ok := tracked[dom][k]; ok || prefNoiseKey.MatchString(k) {
				continue
			}
			if _, domainKnown := before[dom]; !domainKnown {
				continue // brand-new domain: first launch of an app, not a setting change
			}
			out = append(out, PrefChange{Domain: dom, Key: k, Old: old, New: v, Seen: now})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain+out[i].Key < out[j].Domain+out[j].Key })
	return out
}

// capturePrefsInbox diffs every settings domain against the last snapshot and queues real changes for review.
func capturePrefsInbox(d *Data) int {
	if !IsMac {
		return 0
	}
	m, _ := loadMacos(d.Src())
	after := allDomains()
	var before map[string]map[string]any
	if readJSON(prefsSnapPath, &before) != nil {
		_ = writeJSON(prefsSnapPath, after) // first run: baseline only
		return 0
	}
	ignored := map[string]bool{}
	var ign []string
	_ = readJSON(prefsIgnPath, &ign)
	for _, s := range ign {
		ignored[s] = true
	}
	var inbox []PrefChange
	_ = readJSON(prefsInboxPath, &inbox)
	idx := map[string]int{}
	for i, c := range inbox {
		idx[c.Domain+"\x00"+c.Key] = i
	}
	added := 0
	for _, c := range diffSnapshots(before, after, m.Macos.Prefs) {
		id := c.Domain + "\x00" + c.Key
		if ignored[id] {
			continue
		}
		if i, ok := idx[id]; ok {
			inbox[i].New, inbox[i].Seen = c.New, c.Seen
			continue
		}
		inbox = append(inbox, c)
		added++
	}
	_ = writeJSON(prefsInboxPath, inbox)
	_ = writeJSON(prefsSnapPath, after)
	return added
}

// CmdPrefs: `prefs` lists the inbox; `prefs accept N...|all`; `prefs ignore N...|all`; `prefs watch`.
func CmdPrefs(args []string) error {
	if !IsMac {
		return fmt.Errorf("prefs is macOS-only")
	}
	if len(args) > 0 && args[0] == "watch" {
		return prefsWatch()
	}
	var inbox []PrefChange
	_ = readJSON(prefsInboxPath, &inbox)
	if len(args) == 0 {
		if len(inbox) == 0 {
			Say("prefs inbox is empty")
			return nil
		}
		for i, c := range inbox {
			fmt.Printf("%3d  %-42s %-36s %v → %v  %s\n", i+1, c.Domain, c.Key, c.Old, c.New, dim(c.Seen))
		}
		fmt.Println(dim("\n  dotfiles prefs accept 1 3 | all     track and sync these settings"))
		fmt.Println(dim("  dotfiles prefs ignore 2 | all       never suggest them again"))
		return nil
	}
	pick := map[int]bool{}
	for _, a := range args[1:] {
		if a == "all" {
			for i := range inbox {
				pick[i] = true
			}
		}
		var n int
		if _, err := fmt.Sscan(a, &n); err == nil && n >= 1 && n <= len(inbox) {
			pick[n-1] = true
		}
	}
	d, err := LoadData()
	if err != nil {
		return err
	}
	var keep []PrefChange
	var ign []string
	_ = readJSON(prefsIgnPath, &ign)
	m, _ := loadMacos(d.Src())
	n := 0
	for i, c := range inbox {
		if !pick[i] {
			keep = append(keep, c)
			continue
		}
		n++
		if args[0] == "accept" {
			if m.Macos.Prefs[c.Domain] == nil {
				m.Macos.Prefs[c.Domain] = map[string]any{}
			}
			m.Macos.Prefs[c.Domain][c.Key] = jsonToTyped(c.New)
		} else {
			ign = append(ign, c.Domain+"\x00"+c.Key)
		}
	}
	if args[0] == "accept" {
		if _, err := writeMacos(d.Src(), m.Macos.Prefs, m.Macos.Symbolichotkeys.Disabled); err != nil {
			return err
		}
		Say("tracking %d setting(s) in %s", n, macosFile)
	} else {
		_ = writeJSON(prefsIgnPath, ign)
		Say("ignoring %d setting(s)", n)
	}
	return writeJSON(prefsInboxPath, keep)
}

// JSON round-trips numbers as float64; restore ints where the value is integral.
func jsonToTyped(v any) any {
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return int64(f)
	}
	return v
}

func prefsWatch() error {
	d, err := LoadData()
	if err != nil {
		return err
	}
	Say("snapshotting settings…")
	before := allDomains()
	fmt.Print("Change the setting now (System Settings or the app), then press Enter… ")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	m, _ := loadMacos(d.Src())
	changes := diffSnapshots(before, allDomains(), m.Macos.Prefs)
	if len(changes) == 0 {
		Say("no setting changes detected")
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	n := 0
	for _, c := range changes {
		fmt.Printf("  %s  %s: %v → %v   track it? [y/N] ", c.Domain, c.Key, c.Old, c.New)
		ans, _ := in.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(ans)) == "y" {
			if m.Macos.Prefs[c.Domain] == nil {
				m.Macos.Prefs[c.Domain] = map[string]any{}
			}
			m.Macos.Prefs[c.Domain][c.Key] = c.New
			n++
		}
	}
	if n > 0 {
		if _, err := writeMacos(d.Src(), m.Macos.Prefs, m.Macos.Symbolichotkeys.Disabled); err != nil {
			return err
		}
		Say("added %d key(s) to %s", n, macosFile)
	}
	return nil
}

var _ = bytes.MinRead
