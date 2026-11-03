package app

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	keyPath = filepath.Join(Home, ".ssh/id_ed25519")
	pubPath = keyPath + ".pub"
)

func pubKey() string {
	b, err := os.ReadFile(pubPath)
	if err != nil {
		return ""
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return ""
	}
	return f[0] + " " + f[1]
}

// HookKeys makes sure this device has its own SSH key, lists it in ssh_keys.toml,
// and registers it with GitHub for authentication and commit signing.
func HookKeys(d *Data) error {
	if !Exists(keyPath) {
		if err := generateKey(); err != nil {
			return err
		}
	}
	pub := pubKey()
	if pub == "" {
		return nil
	}
	name := Hostname()
	listed := false
	for _, k := range d.SSHKeys {
		if k.Key == pub {
			listed, name = true, k.Name
		}
	}
	if !listed {
		if err := appendSSHKey(d.Src(), SSHKey{Name: name, GithubTitle: name, Key: pub, Added: time.Now().Format("2006-01-02")}); err != nil {
			return err
		}
		Say("added this device's key (%s) to %s; other machines trust it after their next sync", name, sshKeysFile)
	}
	registerGithub(pub, name)
	return nil
}

func generateKey() error {
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	comment := fmt.Sprintf("%s@%s %s", os.Getenv("USER"), Hostname(), time.Now().Format("2006-01-02"))
	if !IsMac {
		Say("generating SSH key for this device")
		return Run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", keyPath)
	}
	// macOS: random passphrase, stored only in the login Keychain (ssh uses it via UseKeychain).
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	pass := base64.RawURLEncoding.EncodeToString(raw)
	Say("generating SSH key for this device (passphrase kept in Keychain)")
	if err := Run("ssh-keygen", "-q", "-t", "ed25519", "-N", pass, "-C", comment, "-f", keyPath); err != nil {
		return err
	}
	askpass := filepath.Join(os.TempDir(), fmt.Sprintf("dotfiles-askpass-%d", os.Getpid()))
	defer os.Remove(askpass)
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\nprintf '%s\\n' \""+pass+"\"\n"), 0o700); err != nil {
		return err
	}
	return Cmd{Args: []string{"ssh-add", "--apple-use-keychain", keyPath},
		Env: []string{"SSH_ASKPASS=" + askpass, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=:0"}}.Stream()
}

type ghKey struct {
	ID    int64  `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

func ghKeys(endpoint string) ([]ghKey, error) {
	out, err := Cmd{Args: []string{"gh", "api", "--paginate", endpoint}}.Output()
	if err != nil {
		return nil, err
	}
	var ks []ghKey
	return ks, json.Unmarshal([]byte(out), &ks)
}

func hasGhKey(ks []ghKey, pub string) bool {
	for _, k := range ks {
		if strings.HasPrefix(k.Key, pub) {
			return true
		}
	}
	return false
}

func registerGithub(pub, title string) {
	done := filepath.Join(StateDir, "github-key-registered")
	if b, _ := os.ReadFile(done); strings.TrimSpace(string(b)) == pub {
		return
	}
	if !Has("gh") || !OK("gh", "auth", "status") {
		Warn("gh not logged in; skipping GitHub key registration (run `gh auth login`, then `chezmoi apply`)")
		return
	}
	tmp := filepath.Join(os.TempDir(), "dotfiles-key.pub")
	_ = os.WriteFile(tmp, []byte(pub+" "+title+"\n"), 0o644)
	defer os.Remove(tmp)
	ok := true
	if auth, err := ghKeys("user/keys"); err == nil && !hasGhKey(auth, pub) {
		if err := Run("gh", "ssh-key", "add", tmp, "--title", title, "--type", "authentication"); err != nil {
			Warn("GitHub auth key: %v", firstLine(err.Error()))
			ok = false
		} else {
			Say("registered %s with GitHub (authentication)", title)
		}
	}
	signing, err := ghKeys("user/ssh_signing_keys")
	if err != nil {
		Warn("can't manage GitHub signing keys yet: run `gh auth refresh -h github.com -s admin:ssh_signing_key`, then `chezmoi apply`")
		return
	}
	if !hasGhKey(signing, pub) {
		if err := Run("gh", "ssh-key", "add", tmp, "--title", title, "--type", "signing"); err != nil {
			Warn("GitHub signing key: %v", firstLine(err.Error()))
			ok = false
		} else {
			Say("registered %s with GitHub (signing)", title)
		}
	}
	if ok {
		_ = os.MkdirAll(StateDir, 0o755)
		_ = os.WriteFile(done, []byte(pub+"\n"), 0o644)
	}
}

// CmdKeys: list | revoke NAME | push HOST | register-all.
func CmdKeys(args []string) error {
	d, err := LoadData()
	if err != nil {
		return err
	}
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		me := pubKey()
		for _, k := range d.SSHKeys {
			mark := "  "
			if k.Key == me {
				mark = "* "
			}
			fmt.Printf("%s%-20s %s…  %s\n", mark, k.Name, k.Key[:min(len(k.Key), 36)], dim("added "+k.Added))
		}
		return nil
	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("usage: dotfiles keys revoke NAME")
		}
		var key string
		for _, k := range d.SSHKeys {
			if k.Name == args[1] {
				key = k.Key
			}
		}
		if key == "" {
			return fmt.Errorf("no device named %q (see `dotfiles keys`)", args[1])
		}
		if !confirm(fmt.Sprintf("Revoke %s everywhere (authorized_keys on all machines + GitHub)?", args[1])) {
			return nil
		}
		if _, err := removeSSHKey(d.Src(), args[1]); err != nil {
			return err
		}
		for _, ep := range []string{"user/keys", "user/ssh_signing_keys"} {
			ks, _ := ghKeys(ep)
			for _, k := range ks {
				if strings.HasPrefix(k.Key, key) {
					_ = Run("gh", "api", "-X", "DELETE", fmt.Sprintf("%s/%d", ep, k.ID))
					Say("deleted from GitHub (%s)", ep)
				}
			}
		}
		Say("removed from %s; machines drop it from authorized_keys on their next sync", sshKeysFile)
		return nil
	case "push":
		if len(args) < 2 {
			return fmt.Errorf("usage: dotfiles keys push HOST")
		}
		pub := pubKey()
		script := fmt.Sprintf(`mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && (grep -qF %s ~/.ssh/authorized_keys || echo %s >> ~/.ssh/authorized_keys)`,
			shq(strings.Fields(pub)[1]), shq(pub+" "+Hostname()))
		if err := Run("ssh", args[1], script); err != nil {
			return err
		}
		Say("this device's key is authorized on %s", args[1])
		return nil
	case "register-all":
		for _, k := range d.SSHKeys {
			registerGithubKey(k)
		}
		return nil
	}
	return fmt.Errorf("usage: dotfiles keys [list | revoke NAME | push HOST | register-all]")
}

// registerGithubKey registers any listed device key as a GitHub signing key.
func registerGithubKey(k SSHKey) {
	signing, err := ghKeys("user/ssh_signing_keys")
	if err != nil {
		Warn("run `gh auth refresh -h github.com -s admin:ssh_signing_key` first")
		return
	}
	if hasGhKey(signing, k.Key) {
		return
	}
	tmp := filepath.Join(os.TempDir(), "dotfiles-key.pub")
	_ = os.WriteFile(tmp, []byte(k.Key+" "+k.Name+"\n"), 0o644)
	defer os.Remove(tmp)
	title := k.GithubTitle
	if title == "" {
		title = k.Name
	}
	if err := Run("gh", "ssh-key", "add", tmp, "--title", title, "--type", "signing"); err != nil {
		Warn("%s: %v", k.Name, firstLine(err.Error()))
	} else {
		Say("registered %s as a GitHub signing key", k.Name)
	}
}
