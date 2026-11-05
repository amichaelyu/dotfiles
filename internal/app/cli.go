package app

import (
	"fmt"
	"os"
)

// Version is set at build time (goreleaser ldflags).
var Version = "dev"

const usage = `dotfiles: set up and keep machines in sync with your chezmoi dotfiles repo

Setup
  setup               first-run wizard: install tools, pick/clone your repo, choose what to install, apply
  config              change this machine's profile, groups, apps and features, then apply

Every day
  status              drift: unsaved changes, package inbox, settings to review, new config folders
  sync                capture → commit → fetch/rebase → leak guard → push → apply
  capture             only pull live changes (configs, settings, packages, Zen) into the repo
  add PATH...         start tracking a file or folder
  exclude [--rm] NAME don't manage a package or app on this machine (no args: list)
  remove PKG...       uninstall here and skip it on this machine (--everywhere: every machine)
  prefs               review settings changed since the last sync (accept N | ignore N | watch)

Accounts, keys and secrets
  login               sign in to everything not signed in yet (--status | --add ID | --skip ID | --reset ID)
  keys                this machine's and your other devices' SSH keys (revoke NAME | push HOST | register-all)
  secrets pull|push   Bitwarden note <-> local secrets cache
  guard [REV]         scan outgoing commits for secrets

Other
  zen-push HOST       copy the whole Zen profile (sessions, logins) to HOST
  zen-backup          back up the Zen profile (tabs, logins, extensions; no caches) to Bitwarden
  zen-restore         restore that backup into this machine's Zen profile
  brewfile            print the Brewfile this machine would install
  hook NAME           run an install step (packages, prefs, zen, agents, keys); called from chezmoi scripts
  version
`

// Main runs the CLI and returns the exit code.
func Main(args []string) int {
	cmd := "help"
	if len(args) > 0 {
		cmd = args[0]
	}
	rest := []string{}
	if len(args) > 1 {
		rest = args[1:]
	}
	err := dispatch(cmd, rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, warnStyle.Render("error:"), err)
		return 1
	}
	return 0
}

func dispatch(cmd string, args []string) error {
	switch cmd {
	case "setup":
		return CmdSetup()
	case "config":
		return CmdConfig()
	case "status":
		return CmdStatus()
	case "sync":
		return CmdSync()
	case "capture":
		d, err := LoadData()
		if err != nil {
			return err
		}
		CmdCapture(d)
		return nil
	case "add":
		return CmdAdd(args)
	case "remove":
		return CmdRemove(args)
	case "exclude":
		return CmdExclude(args)
	case "login":
		return CmdLogin(args)
	case "prefs":
		return CmdPrefs(args)
	case "keys":
		return CmdKeys(args)
	case "secrets":
		return CmdSecrets(args)
	case "guard":
		d, err := LoadData()
		if err != nil {
			return err
		}
		rev := ""
		if len(args) > 0 {
			rev = args[0]
		}
		if !CmdGuard(d, rev) {
			os.Exit(1)
		}
		return nil
	case "zen-push":
		if len(args) == 0 {
			return fmt.Errorf("usage: dotfiles zen-push HOST")
		}
		return CmdZenPush(args[0])
	case "zen-backup":
		return CmdZenBackup()
	case "zen-restore":
		return CmdZenRestore()
	case "hook":
		return runHook(args)
	case "brewfile":
		d, err := LoadData()
		if err != nil {
			return err
		}
		fmt.Print(d.Brewfile())
		return nil
	case "version", "--version", "-v":
		fmt.Println("dotfiles", Version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	fmt.Print(usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func runHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dotfiles hook packages|prefs|zen|agents|keys")
	}
	d, err := LoadData()
	if err != nil {
		return err
	}
	switch args[0] {
	case "packages":
		return HookPackages(d)
	case "prefs":
		return HookPrefs(d)
	case "zen":
		return HookZen(d)
	case "agents":
		return HookAgents(d)
	case "keys":
		return HookKeys(d)
	}
	return fmt.Errorf("unknown hook %q", args[0])
}
