package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// MasApp is a Mac App Store entry.
type MasApp struct {
	Name string `json:"name" toml:"name"`
	ID   int64  `json:"id" toml:"id"`
}

// Group is one package group from packages.toml.
type Group struct {
	Description string   `json:"description"`
	Brew        []string `json:"brew"`
	Cask        []string `json:"cask"`
	Mas         []MasApp `json:"mas"`
	Mise        []string `json:"mise"`
}

// SSHKey is one device key from ssh_keys.toml.
type SSHKey struct {
	Name        string `json:"name"`
	GithubTitle string `json:"github_title"`
	Key         string `json:"key"`
	Added       string `json:"added"`
}

// Data is the merged chezmoi data for this machine (repo .chezmoidata + local config [data]).
type Data struct {
	Chezmoi struct {
		OS          string `json:"os"`
		SourceDir   string `json:"sourceDir"`
		WorkingTree string `json:"workingTree"`
		HomeDir     string `json:"homeDir"`
	} `json:"chezmoi"`
	Packages struct {
		Groups      map[string]Group `json:"groups"`
		Removed     Group            `json:"removed"`
		TrustedTaps struct {
			Taps []string `json:"taps"`
		} `json:"trusted_taps"`
	} `json:"packages"`
	Macos struct {
		Prefs           map[string]map[string]any `json:"prefs"`
		Symbolichotkeys struct {
			Disabled []int `json:"disabled"`
		} `json:"symbolichotkeys"`
	} `json:"macos"`
	SSHKeys  []SSHKey       `json:"ssh_keys"`
	Identity map[string]any `json:"identity"`
	Profile  string         `json:"profile"`
	Groups   []string       `json:"groups"`
	Exclude  []string       `json:"exclude"`
	Autosync bool           `json:"autosync"`
}

// LoadData reads `chezmoi data` for the configured source.
func LoadData() (*Data, error) {
	out, err := Cmd{Args: []string{"chezmoi", "data", "--format", "json"}}.Output()
	if err != nil {
		return nil, err
	}
	var d Data
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return nil, fmt.Errorf("parsing chezmoi data: %w", err)
	}
	return &d, nil
}

// Repo is the dotfiles repo root (chezmoi working tree); Src is the chezmoi source dir inside it.
func (d *Data) Repo() string { return d.Chezmoi.WorkingTree }
func (d *Data) Src() string  { return d.Chezmoi.SourceDir }

// AllGroupNames returns every group name, sorted.
func (d *Data) AllGroupNames() []string {
	names := make([]string, 0, len(d.Packages.Groups))
	for n := range d.Packages.Groups {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// EnabledGroups resolves the profile to the list of groups this machine installs.
func (d *Data) EnabledGroups() []string { return ResolveGroups(d.Profile, d.Groups, d.AllGroupNames()) }

// ResolveGroups: terminal = cli+dev, full = all, custom = cli + chosen.
func ResolveGroups(profile string, chosen, all []string) []string {
	switch profile {
	case "full":
		return all
	case "terminal", "":
		return intersect([]string{"cli", "dev"}, all)
	}
	set := map[string]bool{"cli": true}
	for _, g := range chosen {
		set[g] = true
	}
	return intersect(keys(set), all)
}

// Excluded reports whether a package was deselected on this machine.
func (d *Data) Excluded(name string) bool {
	for _, e := range d.Exclude {
		if e == name || e == short(name) {
			return true
		}
	}
	return false
}

func short(name string) string { return name[strings.LastIndex(name, "/")+1:] }

func intersect(want, all []string) []string {
	have := map[string]bool{}
	for _, a := range all {
		have[a] = true
	}
	var out []string
	for _, w := range want {
		if have[w] {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------- local chezmoi config

// ConfigPath is this machine's chezmoi config.
var ConfigPath = filepath.Join(Home, ".config/chezmoi/chezmoi.toml")

// MachineConfig is what the wizard stores per machine.
type MachineConfig struct {
	SourceDir string
	Profile   string
	Groups    []string
	Exclude   []string
	Autosync  bool
}

// ReadMachineConfig loads the per-machine settings (zero value if none).
func ReadMachineConfig() MachineConfig {
	var raw map[string]any
	if _, err := toml.DecodeFile(ConfigPath, &raw); err != nil {
		return MachineConfig{}
	}
	mc := MachineConfig{}
	mc.SourceDir, _ = raw["sourceDir"].(string)
	if data, ok := raw["data"].(map[string]any); ok {
		mc.Profile, _ = data["profile"].(string)
		mc.Groups = toStrings(data["groups"])
		mc.Exclude = toStrings(data["exclude"])
		mc.Autosync, _ = data["autosync"].(bool)
	}
	return mc
}

// WriteMachineConfig saves per-machine settings, keeping any other keys already in the file.
func WriteMachineConfig(mc MachineConfig) error {
	raw := map[string]any{}
	_, _ = toml.DecodeFile(ConfigPath, &raw)
	raw["sourceDir"] = mc.SourceDir
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	data["profile"] = mc.Profile
	data["groups"] = nonNil(mc.Groups)
	data["exclude"] = nonNil(mc.Exclude)
	data["autosync"] = mc.Autosync
	raw["data"] = data
	if _, ok := raw["diff"]; !ok {
		raw["diff"] = map[string]any{"exclude": []string{"scripts", "externals"}}
	}
	if err := os.MkdirAll(filepath.Dir(ConfigPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(ConfigPath)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "# Written by `dotfiles setup` / `dotfiles config`. Per-machine choices; not in the repo.")
	return toml.NewEncoder(f).Encode(raw)
}

func toStrings(v any) []string {
	arr, _ := v.([]any)
	out := []string{}
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
