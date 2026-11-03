// Package dotfiles embeds the starter template that `dotfiles setup` copies into a new repo.
package dotfiles

import "embed"

// Template holds template/ (the starter chezmoi repo).
//
//go:embed all:template
var Template embed.FS
