package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Secrets live as attachments on one Bitwarden secure note and are cached in SecretsDir (0600).
// Templates in the repo render from the cache: {{ include (joinPath .chezmoi.homeDir ".config/dotfiles/secrets/NAME") }}

func bwItemName(d *Data) string {
	if s, ok := d.Identity["bitwarden_item"].(string); ok && s != "" {
		return s
	}
	return "dotfiles"
}

var secretRef = regexp.MustCompile(`\.config/dotfiles/secrets/([^"]+)"`)

// secretTargets maps cache file name -> live path, read from the repo's templates.
func secretTargets(d *Data) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(d.Src(), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".tmpl") {
			return nil
		}
		b, _ := os.ReadFile(p)
		s := strings.TrimSpace(string(b))
		// only whole-file secrets ({{ include ... }} and nothing else)
		if m := secretRef.FindStringSubmatch(s); m != nil && strings.HasPrefix(s, "{{- include") && strings.Count(s, "{{") == 1 {
			if t, err := (Cmd{Args: []string{"chezmoi", "target-path", p}}).Output(); err == nil {
				out[m[1]] = t
			}
		}
		return nil
	})
	return out
}

// captureSecrets copies secrets changed on this machine into the cache and flags them for `secrets push`.
func captureSecrets(d *Data) []string {
	if !Exists(SecretsDir) {
		return nil
	}
	var changed []string
	for name, target := range secretTargets(d) {
		live, err := os.ReadFile(target)
		if err != nil {
			continue
		}
		cached, _ := os.ReadFile(filepath.Join(SecretsDir, name))
		if !bytes.Equal(live, cached) {
			_ = os.WriteFile(filepath.Join(SecretsDir, name), live, 0o600)
			changed = append(changed, name)
		}
	}
	if len(changed) > 0 {
		_ = os.MkdirAll(StateDir, 0o755)
		_ = os.WriteFile(filepath.Join(StateDir, "secrets-dirty"), []byte(strings.Join(changed, "\n")+"\n"), 0o644)
	}
	return changed
}

// secretNeedles returns the exact secret values in the cache (kept in memory only) for the leak guard.
func secretNeedles() []string {
	seen := map[string]bool{}
	entries, _ := os.ReadDir(SecretsDir)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(SecretsDir, e.Name()))
		if err != nil {
			continue
		}
		var vals []string
		var js any
		if json.Unmarshal(b, &js) == nil {
			walkStrings(js, &vals)
		} else {
			kv := regexp.MustCompile(`^\s*[\w.-]+\s*[=:]\s*(.+)$`)
			for _, line := range strings.Split(string(b), "\n") {
				if m := kv.FindStringSubmatch(line); m != nil {
					vals = append(vals, strings.Trim(strings.TrimSpace(m[1]), `'"`))
				} else {
					vals = append(vals, strings.TrimSpace(line))
				}
			}
		}
		for _, v := range vals {
			for _, piece := range strings.Split(v, `\n`) {
				piece = strings.TrimSpace(piece)
				if len(piece) >= 12 && !strings.Contains(Home, piece) && !strings.HasPrefix(piece, "http") && !strings.HasPrefix(piece, "-----") {
					seen[piece] = true
				}
			}
		}
	}
	return keys(seen)
}

func walkStrings(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for _, y := range x {
			walkStrings(y, out)
		}
	case []any:
		for _, y := range x {
			walkStrings(y, out)
		}
	case string:
		*out = append(*out, x)
	}
}

// ---------------------------------------------------------------------------- Bitwarden

func bwEnv() ([]string, error) {
	if !Has("bw") {
		return nil, fmt.Errorf("Bitwarden CLI (bw) isn't installed")
	}
	if s := os.Getenv("BW_SESSION"); s != "" {
		return []string{"BW_SESSION=" + s}, nil
	}
	var st struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal([]byte(Out("bw", "status")), &st)
	if st.Status == "unauthenticated" {
		Say("logging in to Bitwarden")
		if err := (Cmd{Args: []string{"bw", "login"}}).Stream(); err != nil {
			return nil, err
		}
	}
	Say("unlocking Bitwarden")
	cmd := Cmd{Args: []string{"bw", "unlock", "--raw"}}.build()
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	session := strings.TrimSpace(string(out))
	os.Setenv("BW_SESSION", session) // later Bitwarden steps in this run don't ask again
	env := []string{"BW_SESSION=" + session}
	_, _ = Cmd{Args: []string{"bw", "sync"}, Env: env}.Output()
	return env, nil
}

type bwItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Attachments []struct {
		ID       string `json:"id"`
		FileName string `json:"fileName"`
	} `json:"attachments"`
}

func findBwItem(env []string, name string) (*bwItem, error) {
	out, err := Cmd{Args: []string{"bw", "list", "items", "--search", name}, Env: env}.Output()
	if err != nil {
		return nil, err
	}
	var items []bwItem
	_ = json.Unmarshal([]byte(out), &items)
	for i := range items {
		if items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, nil
}

// CmdSecrets: pull | push.
func CmdSecrets(args []string) error {
	if len(args) == 0 || (args[0] != "pull" && args[0] != "push") {
		return fmt.Errorf("usage: dotfiles secrets pull|push")
	}
	d, err := LoadData()
	if err != nil {
		return err
	}
	name := bwItemName(d)
	env, err := bwEnv()
	if err != nil {
		return err
	}
	item, err := findBwItem(env, name)
	if err != nil {
		return err
	}
	if args[0] == "pull" {
		if item == nil {
			return fmt.Errorf("no Bitwarden item named %q", name)
		}
		if err := os.MkdirAll(SecretsDir, 0o700); err != nil {
			return err
		}
		_ = os.Chmod(filepath.Dir(SecretsDir), 0o700)
		for _, a := range item.Attachments {
			out := filepath.Join(SecretsDir, a.FileName)
			if _, err := (Cmd{Args: []string{"bw", "get", "attachment", a.ID, "--itemid", item.ID, "--output", out}, Env: env}).Output(); err != nil {
				return err
			}
			_ = os.Chmod(out, 0o600)
		}
		_ = os.Remove(filepath.Join(StateDir, "secrets-dirty"))
		Say("pulled %d secret file(s); run `chezmoi apply` to render them", len(item.Attachments))
		return nil
	}

	// push
	if item == nil {
		item, err = createBwNote(env, name, "Secret files for the dotfiles repo. Managed by `dotfiles secrets push`.")
		if err != nil {
			return err
		}
		Say("created Bitwarden secure note %q", name)
	}
	existing := map[string]string{}
	for _, a := range item.Attachments {
		existing[a.FileName] = a.ID
	}
	entries, _ := os.ReadDir(SecretsDir)
	tmp, _ := os.MkdirTemp("", "dotfiles-bw")
	defer os.RemoveAll(tmp)
	uploaded := 0
	for _, e := range entries {
		local := filepath.Join(SecretsDir, e.Name())
		if id, ok := existing[e.Name()]; ok {
			cmp := filepath.Join(tmp, e.Name())
			_, _ = Cmd{Args: []string{"bw", "get", "attachment", id, "--itemid", item.ID, "--output", cmp}, Env: env}.Output()
			a, _ := os.ReadFile(local)
			b, _ := os.ReadFile(cmp)
			if bytes.Equal(a, b) {
				continue
			}
			_, _ = Cmd{Args: []string{"bw", "delete", "attachment", id, "--itemid", item.ID}, Env: env}.Output()
		}
		if _, err := (Cmd{Args: []string{"bw", "create", "attachment", "--file", local, "--itemid", item.ID}, Env: env}).Output(); err != nil {
			return err
		}
		uploaded++
		Say("uploaded %s", e.Name())
	}
	_ = os.Remove(filepath.Join(StateDir, "secrets-dirty"))
	Say("secrets push done (%d uploaded, %d unchanged)", uploaded, len(entries)-uploaded)
	return nil
}

func createBwNote(env []string, name, notes string) (*bwItem, error) {
	var folderID any
	var folders []struct{ ID, Name string }
	_ = json.Unmarshal([]byte(must(Cmd{Args: []string{"bw", "list", "folders"}, Env: env}.Output())), &folders)
	for _, f := range folders {
		if f.Name == name {
			folderID = f.ID
		}
	}
	if folderID == nil {
		enc := must(Cmd{Args: []string{"bw", "encode"}, Stdin: fmt.Sprintf(`{"name":%q}`, name), Env: env}.Output())
		var f struct{ ID string }
		_ = json.Unmarshal([]byte(must(Cmd{Args: []string{"bw", "create", "folder", enc}, Env: env}.Output())), &f)
		folderID = f.ID
	}
	note := map[string]any{"type": 2, "name": name, "folderId": folderID, "secureNote": map[string]any{"type": 0},
		"notes": notes}
	js, _ := json.Marshal(note)
	enc, err := Cmd{Args: []string{"bw", "encode"}, Stdin: string(js), Env: env}.Output()
	if err != nil {
		return nil, err
	}
	out, err := Cmd{Args: []string{"bw", "create", "item", enc}, Env: env}.Output()
	if err != nil {
		return nil, err
	}
	var it bwItem
	return &it, json.Unmarshal([]byte(out), &it)
}

func must(s string, _ error) string { return s }
