package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// Template is the starter repo embedded in the binary (set from the module root package).
var Template fs.FS

var titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13")).MarginBottom(1)

func confirm(q string) bool {
	if os.Getenv("DOTFILES_YES") == "1" { // non-interactive runs (tests, scripts)
		Say("%s yes (DOTFILES_YES)", q)
		return true
	}
	ok := false
	if err := huh.NewConfirm().Title(q).Value(&ok).Run(); err != nil {
		return false
	}
	return ok
}

// ---------------------------------------------------------------------------- prerequisites

func ensureLocalBinOnPath() {
	lb := filepath.Join(Home, ".local/bin")
	_ = os.MkdirAll(lb, 0o755)
	if !strings.Contains(os.Getenv("PATH"), lb) {
		os.Setenv("PATH", lb+":"+os.Getenv("PATH"))
	}
}

func ensurePrereqs() error {
	ensureLocalBinOnPath()
	if IsMac {
		if !OK("xcode-select", "-p") {
			Say("installing Xcode Command Line Tools (a system dialog will open)")
			_ = Run("xcode-select", "--install")
			for !OK("xcode-select", "-p") {
				time.Sleep(5 * time.Second)
			}
		}
		if err := ensureBrew(); err != nil {
			return err
		}
		// command -> Homebrew formula. bw comes first so secrets can be pulled before the big install.
		var missing []string
		for _, t := range [][2]string{{"bw", "bitwarden-cli"}, {"git", "git"}, {"gh", "gh"}, {"chezmoi", "chezmoi"},
			{"jj", "jj"}, {"uv", "uv"}, {"gitleaks", "gitleaks"}} {
			if !Has(t[0]) {
				missing = append(missing, t[1])
			}
		}
		if len(missing) > 0 {
			Say("installing %s", strings.Join(missing, ", "))
			return Cmd{Args: append([]string{"brew", "install"}, missing...)}.Stream()
		}
		return nil
	}
	if !Has("chezmoi") {
		Say("installing chezmoi")
		if err := (Cmd{Args: []string{"sh", "-c", `sh -c "$(curl -fsLS get.chezmoi.io)" -- -b "$HOME/.local/bin"`}}).Stream(); err != nil {
			return err
		}
	}
	if !Exists(MiseBin) && !Has("mise") {
		Say("installing mise")
		if err := (Cmd{Args: []string{"sh", "-c", "curl -fsSL https://mise.run | sh"}}).Stream(); err != nil {
			return err
		}
	}
	var missing []string
	for _, t := range []string{"gh", "jj", "uv", "gitleaks"} {
		if !Has(t) {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		Say("installing %s with mise", strings.Join(missing, ", "))
		_ = Cmd{Args: append([]string{MiseBin, "use", "-g", "--yes"}, missing...)}.Stream()
		os.Setenv("PATH", filepath.Join(Home, ".local/share/mise/shims")+":"+os.Getenv("PATH"))
	}
	return nil
}

func ghUser() string {
	if Has("gh") && OK("gh", "auth", "status") {
		return Out("gh", "api", "user", "-q", ".login")
	}
	return ""
}

func ensureGhAuth() error {
	if OK("gh", "auth", "status") {
		return nil
	}
	Say("log in to GitHub (needed to clone a private repo and register this device's SSH key)")
	return Cmd{Args: []string{"gh", "auth", "login", "--git-protocol", "https", "--scopes", "admin:public_key,admin:ssh_signing_key"}}.Stream()
}

// ---------------------------------------------------------------------------- repo

func chooseRepo(mc *MachineConfig) error {
	if mc.SourceDir != "" && Exists(mc.SourceDir) {
		keep := true
		_ = huh.NewConfirm().Title("Use your dotfiles repo at " + mc.SourceDir + "?").Affirmative("Yes").Negative("Choose another").Value(&keep).Run()
		if keep {
			return nil
		}
	}
	mode := "clone"
	if err := huh.NewSelect[string]().Title("Where are your dotfiles?").Options(
		huh.NewOption("I have a dotfiles repo on GitHub (made with this tool)", "clone"),
		huh.NewOption("Start a new one from the starter template", "new"),
	).Value(&mode).Run(); err != nil {
		return err
	}
	if err := ensureGhAuth(); err != nil {
		return err
	}
	user := ghUser()
	if mode == "clone" {
		repo := user + "/dotfiles-private"
		dir := filepath.Join(Home, ".local/share/chezmoi")
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("GitHub repo").Value(&repo),
			huh.NewInput().Title("Clone into").Value(&dir),
		)).Run(); err != nil {
			return err
		}
		dir = ExpandHome(dir)
		if !Exists(dir) {
			Say("cloning %s", repo)
			if err := (Cmd{Args: []string{"gh", "repo", "clone", repo, dir}}).Stream(); err != nil {
				return err
			}
			if Has("jj") {
				_ = Cmd{Args: []string{"jj", "git", "init", "--colocate"}, Dir: dir}.Stream()
			}
		}
		mc.SourceDir = dir
		return nil
	}
	return newFromTemplate(mc, user)
}

func newFromTemplate(mc *MachineConfig, user string) error {
	name, email, dir, repo := Out("git", "config", "--global", "user.name"), Out("git", "config", "--global", "user.email"), filepath.Join(Home, ".local/share/chezmoi"), "dotfiles-private"
	if user != "" && email == "" {
		email = user + "@users.noreply.github.com"
	}
	createRemote := true
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Your name").Value(&name),
		huh.NewInput().Title("Git email").Value(&email),
		huh.NewInput().Title("Create the repo in").Value(&dir),
		huh.NewInput().Title("GitHub repo name (private)").Value(&repo),
		huh.NewConfirm().Title("Create the private GitHub repo now?").Value(&createRemote),
	)).Run(); err != nil {
		return err
	}
	dir = ExpandHome(dir)
	if Exists(dir) {
		return fmt.Errorf("%s already exists", dir)
	}
	if err := fs.WalkDir(Template, "template", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, "template"), "/")
		dst := filepath.Join(dir, rel)
		if e.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(Template, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(filepath.Base(p), "executable_") || strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(dst, b, mode)
	}); err != nil {
		return err
	}
	identity := fmt.Sprintf("# Who you are. Used by the git/jj templates and allowed_signers.\n[identity]\nname = %s\nemail = %s\ngithub = %s\nbitwarden_item = \"dotfiles\"\n", q(name), q(email), q(user))
	if err := os.WriteFile(filepath.Join(dir, "home/.chezmoidata/identity.toml"), []byte(identity), 0o644); err != nil {
		return err
	}
	if Has("jj") {
		_ = Cmd{Args: []string{"jj", "git", "init", "--colocate"}, Dir: dir}.Stream()
	} else {
		_ = Cmd{Args: []string{"git", "init", "-q"}, Dir: dir}.Stream()
	}
	if createRemote && user != "" {
		if err := (Cmd{Args: []string{"gh", "repo", "create", repo, "--private"}}).Stream(); err == nil {
			remote := fmt.Sprintf("https://github.com/%s/%s.git", user, repo)
			if Has("jj") {
				_ = Run("jj", "-R", dir, "git", "remote", "add", "origin", remote)
			} else {
				_ = Run("git", "-C", dir, "remote", "add", "origin", remote)
			}
		}
	}
	mc.SourceDir = dir
	Say("new dotfiles repo at %s", dir)
	return nil
}

// ---------------------------------------------------------------------------- choices

func chooseProfile(mc *MachineConfig) error {
	// Point chezmoi at the repo so `chezmoi data` sees its package groups.
	if err := WriteMachineConfig(*mc); err != nil {
		return err
	}
	d, err := LoadData()
	if err != nil {
		return err
	}
	all := d.AllGroupNames()
	def := "full"
	if !IsMac {
		def = "terminal"
	}
	if mc.Profile == "" {
		mc.Profile = def
	}
	if err := huh.NewSelect[string]().Title("What should this machine get?").Options(
		huh.NewOption("Terminal only: shell, CLI tools, dev toolchains", "terminal"),
		huh.NewOption("Everything: all apps and settings", "full"),
		huh.NewOption("Custom: pick groups", "custom"),
	).Value(&mc.Profile).Run(); err != nil {
		return err
	}
	if mc.Profile == "custom" {
		var opts []huh.Option[string]
		for _, g := range all {
			if g == "cli" {
				continue
			}
			grp := d.Packages.Groups[g]
			n := len(grp.Brew) + len(grp.Cask) + len(grp.Mas) + len(grp.Mise)
			opts = append(opts, huh.NewOption(fmt.Sprintf("%-10s %s (%d)", g, grp.Description, n), g).Selected(contains(mc.Groups, g)))
		}
		if err := huh.NewMultiSelect[string]().Title("Groups (cli is always included)").Options(opts...).Value(&mc.Groups).Run(); err != nil {
			return err
		}
	}
	tune := false
	_ = huh.NewConfirm().Title("Fine-tune individual apps?").Value(&tune).Run()
	if !tune {
		return nil
	}
	excluded := set(mc.Exclude)
	shown := map[string]bool{}
	var newExclude []string
	for _, g := range ResolveGroups(mc.Profile, mc.Groups, all) {
		grp := d.Packages.Groups[g]
		var names []string
		if IsMac {
			names = append(append(names, grp.Brew...), grp.Cask...)
			for _, m := range grp.Mas {
				names = append(names, m.Name)
			}
		} else {
			names = grp.Mise
		}
		if len(names) == 0 {
			continue
		}
		var opts []huh.Option[string]
		var keep []string
		for _, n := range names {
			opts = append(opts, huh.NewOption(n, n).Selected(!excluded[n]))
		}
		if err := huh.NewMultiSelect[string]().Title(fmt.Sprintf("%s: %s", g, grp.Description)).Options(opts...).Height(min(len(opts)+2, 18)).Value(&keep).Run(); err != nil {
			return err
		}
		kept := set(keep)
		for _, n := range names {
			shown[n] = true
			if !kept[n] {
				newExclude = append(newExclude, n)
			}
		}
	}
	for _, e := range mc.Exclude { // keep excludes added by hand (apps, wildcards, other groups)
		if !shown[e] {
			newExclude = append(newExclude, e)
		}
	}
	mc.Exclude = newExclude
	return nil
}

func chooseFeatures(mc *MachineConfig, d *Data) error {
	if err := huh.NewConfirm().Title("Daily auto-sync?").
		Description("Every day: capture changes (configs, apps, settings) → commit → leak check → push → apply updates from your other machines.").
		Value(&mc.Autosync).Run(); err != nil {
		return err
	}
	if !Exists(SecretsDir) && hasSecretTemplates(d) {
		pull := true
		desc := "Yes signs you in with the Bitwarden CLI (bw) and downloads your secret files."
		if !Has("bw") {
			desc = "Yes installs the Bitwarden CLI (bw) with mise, then signs you in to Bitwarden."
		}
		_ = huh.NewConfirm().Title("Pull your secrets from Bitwarden now?").Description(desc).Value(&pull).Run()
		if pull {
			// macOS installs bw with the prerequisites; Linux (or a failed install) gets it here.
			if err := ensureBw(); err != nil {
				Warn("couldn't install the Bitwarden CLI: %v (run `dotfiles secrets pull` after setup)", err)
			} else if err := CmdSecrets([]string{"pull"}); err != nil {
				Warn("secrets: %v (run `dotfiles secrets pull` later)", err)
			}
		}
	}
	return nil
}

func hasSecretTemplates(d *Data) bool { return len(secretTargets(d)) > 0 }

// ensureAppStoreSignIn waits for an App Store sign-in before the install, so App Store apps
// (Xcode, ...) don't fail. Skippable; the packages hook then leaves them out until you sign in.
func ensureAppStoreSignIn(apps []MasApp) {
	if len(apps) == 0 || appStoreSignedIn() != no {
		return // nothing to install, already signed in, or can't tell (the hook just tries)
	}
	names := make([]string, len(apps))
	for i, a := range apps {
		names[i] = a.Name
	}
	Say("sign in to the App Store with your Apple ID (needed for %s)", strings.Join(names, ", "))
	_ = Run("open", "-a", "App Store")
	for {
		Say("waiting for the App Store sign-in...")
		for i := 0; i < 150; i++ { // 5 minutes
			if appStoreSignedIn() == yes {
				Say("signed in to the App Store")
				return
			}
			time.Sleep(2 * time.Second)
		}
		if !confirm("Still not signed in to the App Store. Keep waiting? (No = install App Store apps later)") {
			Warn("skipping App Store apps for now; sign in and run `chezmoi apply`")
			return
		}
	}
}

// ensureBw installs the Bitwarden CLI: Homebrew on macOS, mise on Linux (same names as packages.toml).
func ensureBw() error {
	if Has("bw") {
		return nil
	}
	Say("installing the Bitwarden CLI")
	if IsMac {
		return Cmd{Args: []string{"brew", "install", "bitwarden-cli"}}.Stream()
	}
	if err := (Cmd{Args: []string{MiseBin, "use", "-g", "--yes", "bitwarden"}}).Stream(); err != nil {
		return err
	}
	os.Setenv("PATH", filepath.Join(Home, ".local/share/mise/shims")+":"+os.Getenv("PATH"))
	if !Has("bw") {
		return fmt.Errorf("installed, but bw isn't on PATH")
	}
	return nil
}

// ---------------------------------------------------------------------------- commands

// CmdSetup is the full first-run wizard: prerequisites, repo, choices, apply.
func CmdSetup() error {
	fmt.Println(titleStyle.Render("dotfiles setup"))
	if err := ensurePrereqs(); err != nil {
		return err
	}
	mc := ReadMachineConfig()
	if err := chooseRepo(&mc); err != nil {
		return err
	}
	return configureAndApply(&mc)
}

// CmdConfig re-runs the choices (profile, groups, apps, features) for this machine and applies.
func CmdConfig() error {
	mc := ReadMachineConfig()
	if mc.SourceDir == "" {
		return errors.New("no dotfiles repo configured yet; run `dotfiles setup`")
	}
	return configureAndApply(&mc)
}

func configureAndApply(mc *MachineConfig) error {
	if err := chooseProfile(mc); err != nil {
		return err
	}
	d, err := LoadData()
	if err != nil {
		return err
	}
	if err := chooseFeatures(mc, d); err != nil {
		return err
	}
	if err := WriteMachineConfig(*mc); err != nil {
		return err
	}
	if IsMac {
		if d, err := LoadData(); err == nil { // reload: groups and excludes were just chosen
			ensureAppStoreSignIn(d.masApps())
		}
	}
	Say("applying (this installs packages and can take a while)")
	if err := (Cmd{Args: []string{"chezmoi", "apply", "--verbose=false"}}).Stream(); err != nil {
		return err
	}
	fmt.Println()
	// Zen has no profile until it's first opened, so its config couldn't be applied yet. Restore your
	// profile from Bitwarden (before sign-ins, so browser flows find you logged in), or start fresh.
	if IsMac && Exists("/Applications/Zen.app") && zenProfile() == "" {
		if d, err := LoadData(); err == nil {
			restored := false
			if Has("bw") && confirm("Restore your Zen profile (tabs, logins, extensions) from Bitwarden?") {
				if !zenBackupExists(d) {
					Warn("no Zen backup in Bitwarden yet (`dotfiles zen-backup` on your other Mac)")
				} else if err := CmdZenRestore(); err != nil {
					Warn("Zen restore: %v", err)
				} else {
					restored = true
				}
			}
			if !restored {
				if _, err := ensureZenProfile(); err != nil {
					Warn("Zen: %v", err)
				} else {
					_ = HookZen(d)
				}
			}
		}
	}
	if confirm("Sign in to your apps now? (`dotfiles login`; you can run it later too)") {
		if err := CmdLogin(nil); err != nil {
			Warn("login: %v", err)
		}
	}
	Say("done. Useful next: `dotfiles status`, `dotfiles config` to change choices, `dotfiles help`")
	return nil
}
