package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// `dotfiles zen-backup` / `zen-restore`: the parts of the Zen profile worth keeping (history,
// bookmarks, cookies, saved passwords, permissions, open tabs and workspaces, extensions and their
// settings) as one tar.gz attached to a Bitwarden secure note, which Bitwarden encrypts. Caches,
// translation models, site offline data and session backups are left out; Zen rebuilds them.

const zenBackupFile = "zen-profile.tar.gz"

// zenBackupItem is a separate note from the secrets one, so `secrets pull` doesn't fetch the profile.
func zenBackupItem(d *Data) string { return bwItemName(d) + "-zen" }

// Top-level profile entries left out of the backup.
var zenSkip = map[string]bool{
	"cache2": true, "startupCache": true, "shader-cache": true, "thumbnails": true, "safebrowsing": true,
	"sessionstore-backups": true, "zen-sessions-backup": true, "sessionstore-logs": true,
	"minidumps": true, "crashes": true, "datareporting": true, "saved-telemetry-pings": true,
	"security_state": true, "gmp": true, "gmp-gmpopenh264": true, "gmp-widevinecdm": true,
	"lock": true, ".parentlock": true, "parent.lock": true,
	"suggest.sqlite": true, "suggest.sqlite-wal": true, "suggest.sqlite-shm": true,
	"domain_to_categories.sqlite": true, "domain_to_categories.sqlite-journal": true,
}

// zenBackupPaths lists what goes in the archive, relative to the profile. From storage/ only the
// extensions' own data (moz-extension+++*) is kept; websites' storage and Zen's downloads aren't.
func zenBackupPaths(prof string) ([]string, error) {
	entries, err := os.ReadDir(prof)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		n := e.Name()
		if zenSkip[n] || n == "storage" || n == ".DS_Store" {
			continue
		}
		paths = append(paths, n)
	}
	ext, _ := filepath.Glob(filepath.Join(prof, "storage/default/moz-extension+++*"))
	for _, p := range ext {
		rel, _ := filepath.Rel(prof, p)
		paths = append(paths, rel)
	}
	return paths, nil
}

// quitZenForCopy quits Zen (after asking) so its databases are consistent on disk.
// Returns whether Zen was running, so the caller can reopen it.
func quitZenForCopy(why string) (bool, error) {
	if !zenRunning() {
		return false, nil
	}
	if !confirm(fmt.Sprintf("Zen has to quit to %s (your tabs come back). Quit it now?", why)) {
		return false, fmt.Errorf("cancelled; quit Zen and run this again")
	}
	_ = Run("osascript", "-e", `quit app "Zen"`)
	for i := 0; i < 60 && zenRunning(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if zenRunning() {
		return true, fmt.Errorf("Zen didn't quit; close it and try again")
	}
	return true, nil
}

// CmdZenBackup archives the profile and uploads it to Bitwarden, replacing the previous backup.
func CmdZenBackup() error {
	d, err := LoadData()
	if err != nil {
		return err
	}
	prof := zenProfile()
	if prof == "" {
		return fmt.Errorf("no Zen profile found")
	}
	paths, err := zenBackupPaths(prof)
	if err != nil {
		return err
	}
	env, err := bwEnv()
	if err != nil {
		return err
	}
	wasRunning, err := quitZenForCopy("back up its profile")
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "dotfiles-zen")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, zenBackupFile)
	Say("archiving %s", filepath.Base(prof))
	tarErr := Cmd{Args: append([]string{"tar", "-czf", archive, "-C", prof, "--exclude", "storage/default/*/cache"}, paths...)}.Stream()
	if wasRunning {
		_ = Run("open", "-a", "Zen")
	}
	if tarErr != nil {
		return tarErr
	}
	_ = os.Chmod(archive, 0o600)
	st, _ := os.Stat(archive)

	name := zenBackupItem(d)
	item, err := findBwItem(env, name)
	if err != nil {
		return err
	}
	if item == nil {
		if item, err = createBwNote(env, name, "Zen browser profile backup. Managed by `dotfiles zen-backup`."); err != nil {
			return err
		}
		Say("created Bitwarden secure note %q", name)
	}
	Say("uploading %.0f MB to Bitwarden", float64(st.Size())/1e6)
	if _, err := (Cmd{Args: []string{"bw", "create", "attachment", "--file", archive, "--itemid", item.ID}, Env: env}).Output(); err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	// Remove older copies only after the new one is safely uploaded.
	for _, a := range item.Attachments {
		if a.FileName == zenBackupFile {
			_, _ = Cmd{Args: []string{"bw", "delete", "attachment", a.ID, "--itemid", item.ID}, Env: env}.Output()
		}
	}
	Say("Zen profile backed up to Bitwarden (%q)", name)
	return nil
}

// ensureZenProfile starts Zen headless once so it creates its default profile (fresh machines).
func ensureZenProfile() (string, error) {
	if p := zenProfile(); p != "" {
		return p, nil
	}
	bin := "/Applications/Zen.app/Contents/MacOS/zen"
	if !IsMac {
		bin = "zen"
	}
	Say("creating a Zen profile")
	cmd := exec.Command(bin, "--headless", "about:blank")
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("couldn't start Zen: %w", err)
	}
	var prof string
	for i := 0; i < 60; i++ {
		if prof = zenProfile(); prof != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // let it finish writing the new profile
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	if prof == "" {
		return "", fmt.Errorf("Zen didn't create a profile")
	}
	return prof, nil
}

// zenBackupCache holds a backup downloaded ahead of time (setup fetches it with the secrets, so
// Bitwarden is only needed once). It's removed after a successful restore.
var zenBackupCache = filepath.Join(StateDir, zenBackupFile)

// fetchZenBackup downloads the backup from the Bitwarden note itemName to dst. false if there's none.
func fetchZenBackup(env []string, itemName, dst string) (bool, error) {
	item, err := findBwItem(env, itemName)
	if err != nil || item == nil {
		return false, err
	}
	for _, a := range item.Attachments {
		if a.FileName != zenBackupFile {
			continue
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		if _, err := (Cmd{Args: []string{"bw", "get", "attachment", a.ID, "--itemid", item.ID, "--output", dst}, Env: env}).Output(); err != nil {
			return false, fmt.Errorf("download failed: %w", err)
		}
		_ = os.Chmod(dst, 0o600)
		return true, nil
	}
	return false, nil
}

// CmdZenRestore unpacks the backup (already downloaded, or fetched now) into the Zen profile. The
// current profile folder is kept next to it (renamed) rather than deleted.
func CmdZenRestore() error {
	d, err := LoadData()
	if err != nil {
		return err
	}
	archive := zenBackupCache
	if !Exists(archive) {
		env, err := bwEnv()
		if err != nil {
			return err
		}
		tmp, err := os.MkdirTemp("", "dotfiles-zen")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		archive = filepath.Join(tmp, zenBackupFile)
		Say("downloading the Zen backup")
		found, err := fetchZenBackup(env, zenBackupItem(d), archive)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("no Zen backup in Bitwarden yet; run `dotfiles zen-backup` on a machine that has your profile")
		}
	}
	if _, err := quitZenForCopy("restore its profile"); err != nil {
		return err
	}
	prof, err := ensureZenProfile()
	if err != nil {
		return err
	}

	aside := prof + ".before-restore-" + time.Now().Format("2006-01-02-150405")
	if err := os.Rename(prof, aside); err != nil {
		return err
	}
	if err := os.MkdirAll(prof, 0o700); err != nil {
		return err
	}
	if err := (Cmd{Args: []string{"tar", "-xzf", archive, "-C", prof}}).Stream(); err != nil {
		_ = os.RemoveAll(prof)
		_ = os.Rename(aside, prof)
		return fmt.Errorf("unpacking failed, previous profile put back: %w", err)
	}
	_ = os.Remove(zenBackupCache)
	Say("Zen profile restored; the previous one is at %s", strings.TrimPrefix(aside, Home+"/"))
	return HookZen(d) // re-apply the repo's .zen config on top
}
