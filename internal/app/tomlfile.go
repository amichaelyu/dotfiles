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

// Data files in the user's repo, relative to the chezmoi source dir.
const (
	packagesFile = ".chezmoidata/packages.toml"
	inboxFile    = ".chezmoidata/inbox.toml"
	macosFile    = ".chezmoidata/macos.toml"
	sshKeysFile  = ".chezmoidata/ssh_keys.toml"
)

func q(s string) string { b, _ := json.Marshal(s); return string(b) }

func tomlValue(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%.1f", x)
		}
		return fmt.Sprintf("%v", x)
	case int, int64, uint64:
		return fmt.Sprintf("%d", x)
	case string:
		return q(x)
	}
	return q(fmt.Sprint(v))
}

// writeInbox regenerates inbox.toml.
func writeInbox(src string, g Group) (bool, error) {
	var b strings.Builder
	b.WriteString("# Installed on some machine but not yet sorted into a group in packages.toml.\n")
	b.WriteString("# `dotfiles sync` appends here; `full` machines install it. Move entries into a group when you can.\n\n")
	b.WriteString("[packages.groups.inbox]\n")
	b.WriteString(`description = "Unsorted (auto-captured)"` + "\n")
	for _, kv := range []struct {
		k string
		v []string
	}{{"brew", g.Brew}, {"cask", g.Cask}, {"mise", g.Mise}} {
		vals := append([]string(nil), kv.v...)
		sort.Strings(vals)
		quoted := make([]string, len(vals))
		for i, v := range vals {
			quoted[i] = q(v)
		}
		fmt.Fprintf(&b, "%s = [%s]\n", kv.k, strings.Join(quoted, ", "))
	}
	if len(g.Mas) > 0 {
		b.WriteString("mas = [\n")
		for _, m := range g.Mas {
			fmt.Fprintf(&b, "  { name = %s, id = %d },\n", q(m.Name), m.ID)
		}
		b.WriteString("]\n")
	}
	return WriteIfChanged(filepath.Join(src, inboxFile), b.String())
}

// writeMacos regenerates macos.toml from prefs (domain -> key -> value) and disabled hotkeys.
func writeMacos(src string, prefs map[string]map[string]any, hotkeys []int) (bool, error) {
	var b strings.Builder
	b.WriteString("# Curated macOS / app settings, applied with typed `defaults write` on desktop machines.\n")
	b.WriteString("# `dotfiles sync` refreshes these values; `dotfiles prefs` reviews newly changed settings.\n")
	b.WriteString("# Domains prefixed with currentHost: are written with -currentHost.\n\n")
	for _, dom := range keys(prefs) {
		fmt.Fprintf(&b, "[macos.prefs.%s]\n", q(dom))
		for _, k := range keys(prefs[dom]) {
			fmt.Fprintf(&b, "%s = %s\n", q(k), tomlValue(prefs[dom][k]))
		}
		b.WriteString("\n")
	}
	hk := make([]string, len(hotkeys))
	for i, h := range hotkeys {
		hk[i] = fmt.Sprint(h)
	}
	b.WriteString("[macos.symbolichotkeys]\n# System shortcuts turned off. 64 = Spotlight (⌘Space)\n")
	fmt.Fprintf(&b, "disabled = [%s]\n", strings.Join(hk, ", "))
	return WriteIfChanged(filepath.Join(src, macosFile), b.String())
}

// moveToRemoved drops name from every group array in packages.toml and lists it under [packages.removed].
func moveToRemoved(src, kind, name string) error {
	path := filepath.Join(src, packagesFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(b)
	qn := regexp.QuoteMeta(q(name))
	text = regexp.MustCompile(`\s*`+qn+`,`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`,?\s*`+qn+`(\s*\])`).ReplaceAllString(text, "$1")
	re := regexp.MustCompile(`(?ms)(\[packages\.removed\].*?^` + kind + ` = \[)([^\]]*)\]`)
	if m := re.FindStringSubmatchIndex(text); m != nil {
		items := []string{}
		for _, s := range strings.Split(text[m[4]:m[5]], ",") {
			if s = strings.TrimSpace(s); s != "" {
				items = append(items, s)
			}
		}
		items = append(items, q(name))
		text = text[:m[4]] + strings.Join(items, ", ") + text[m[5]:]
	} else if !strings.Contains(text, "[packages.removed]") {
		text += fmt.Sprintf("\n[packages.removed]\n%s = [%s]\n", kind, q(name))
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// appendSSHKey adds a device key to ssh_keys.toml.
func appendSSHKey(src string, k SSHKey) error {
	path := filepath.Join(src, sshKeysFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n[[ssh_keys]]\nname = %s\ngithub_title = %s\nkey = %s\nadded = %s\n", q(k.Name), q(k.GithubTitle), q(k.Key), q(k.Added))
	return err
}

// removeSSHKey deletes the [[ssh_keys]] block with the given name.
func removeSSHKey(src, name string) (bool, error) {
	path := filepath.Join(src, sshKeysFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	blocks := strings.Split(string(b), "[[ssh_keys]]")
	out := []string{blocks[0]}
	found := false
	for _, blk := range blocks[1:] {
		if regexp.MustCompile(`(?m)^name = ` + regexp.QuoteMeta(q(name)) + `\s*$`).MatchString(blk) {
			found = true
			continue
		}
		out = append(out, blk)
	}
	if !found {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(out, "[[ssh_keys]]")), 0o644)
}
