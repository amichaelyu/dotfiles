# macOS-only shell setup, sourced early from ~/.zshrc.

# Homebrew: static PATH instead of the slow `eval $(brew shellenv)`
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$HOME/.local/bin:$PATH"
export MANPATH="/opt/homebrew/share/man:$MANPATH"
export INFOPATH="/opt/homebrew/share/info:$INFOPATH"
# Homebrew completions (normally added by `brew shellenv`, which we skip above)
typeset -U fpath
fpath+=(/opt/homebrew/share/zsh/site-functions)

export CLIPCOPY="pbcopy"
export LS_PREVIEW="ls -1G"
