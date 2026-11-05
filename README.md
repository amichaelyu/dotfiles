# dotfiles

One command to set up a Mac or Linux machine from your own dotfiles repo, then keep every
machine in sync on its own. Built on [chezmoi](https://chezmoi.io).

```sh
curl -fsSL https://raw.githubusercontent.com/amichaelyu/dotfiles/main/install.sh | sh
```

The installer drops a single `dotfiles` binary into `~/.local/bin` and starts the setup wizard:

1. **Tools**: Xcode CLT + Homebrew on macOS, mise on Linux (no sudo needed), plus git, gh, chezmoi, jj.
2. **Your repo**: clone your existing dotfiles repo (default `<your GitHub username>/dotfiles-private`),
   or start a private one with that name from the built-in starter template.
3. **What this machine gets**: *terminal* (CLI tools only), *everything*, or *custom* groups, with
   optional per-app fine-tuning.
4. **Features**: daily auto-sync, Bitwarden secrets, a fresh SSH key for this device.

Change your mind later with `dotfiles config`.

## What it does for you

| | |
|---|---|
| **Packages** | Groups in `packages.toml` (cli, dev, desktop, …) install via Homebrew/App Store on macOS and mise on Linux. |
| **New apps** | Daily sync notices installs and files them in `inbox.toml`. Uninstalling an app only affects that machine (it's excluded there); `dotfiles remove PKG --everywhere` drops it from every machine. Drag-installed apps are adopted as Homebrew casks when one matches. |
| **Settings** | Curated `defaults` keys in `macos.toml` are applied and kept current. Every other setting you change is diffed daily and queued for review (`dotfiles prefs`). |
| **SSH keys** | Each device gets its own key, registered with GitHub for login and commit signing. `authorized_keys` and `allowed_signers` are generated on every machine; `dotfiles keys revoke` removes a lost device everywhere. |
| **Accounts** | `dotfiles login` signs in everything that isn't yet: CLI tools from Bitwarden secrets or their own login; browser-based apps (Slack, Figma, Zed, Raycast, Zoom) by opening each app or its sign-in page for you to finish, then noticing when you're in; QR codes for Discord/Steam; and a short checklist for the rest. |
| **Secrets** | Kept as attachments on one Bitwarden note, cached locally (0600), rendered into config files by chezmoi. Never in git. |
| **Sync** | Daily: capture → commit (jj) → fetch/rebase → leak guard (gitleaks + exact match on your secrets) → push → apply. |

## Commands

```
dotfiles setup | config                  first run / change this machine's choices
dotfiles status                          what's drifted or waiting for review
dotfiles sync | capture                  full daily sync / just pull live changes into the repo
dotfiles prefs [accept N | ignore N | watch]
dotfiles login [--status | --add ID | --skip ID]
dotfiles exclude [--rm] NAME            don't manage a package/app on this machine
dotfiles keys [revoke NAME | push HOST]
dotfiles secrets pull | push
dotfiles add PATH                        start tracking a file
dotfiles remove PKG [--everywhere]       uninstall here (or on every machine)
dotfiles zen-push HOST                   copy your whole Zen browser profile to another machine
dotfiles zen-backup | zen-restore        Zen profile (tabs, logins, extensions; no caches) <-> Bitwarden
```

## Your repo

Your dotfiles stay in **your own private repo**, `<your GitHub username>/dotfiles-private` by default
(the wizard fills in the username from your `gh` login). This repo only holds the tool and the starter
template (`template/`). Layout of a dotfiles repo:

```
home/                         chezmoi source (.chezmoiroot)
  .chezmoidata/               packages.toml, inbox.toml, macos.toml, ssh_keys.toml, identity.toml
  .chezmoiscripts/            one-line hooks: `dotfiles hook packages|prefs|zen|keys|agents`
  .zen/                       (optional) Zen browser user.js, chrome/, themes
  dot_zshrc, dot_config/…     your configs, as usual for chezmoi
```

Per-machine choices (profile, groups, excluded apps, auto-sync) live in `~/.config/chezmoi/chezmoi.toml`,
not in the repo.

## Development

```sh
go build ./cmd/dotfiles && ./dotfiles help
```

Releases: push a `v*` tag; GitHub Actions runs goreleaser and publishes macOS/Linux × arm64/amd64 binaries.
