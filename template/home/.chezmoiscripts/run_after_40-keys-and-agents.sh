#!/bin/sh
# Every apply: make sure this device has its own SSH key (registered with GitHub + listed in
# ssh_keys.toml), and (re)load LaunchAgents / systemd timers whose files changed. Both are no-ops when nothing changed.
PATH="$HOME/.local/bin:$PATH"
dotfiles hook keys
dotfiles hook agents
