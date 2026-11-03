# Linux-only shell setup, sourced early from ~/.zshrc.

export PATH="$HOME/.local/bin:$PATH"

# Tools come from mise (see ~/.config/mise/config.toml)
if [ -x "$HOME/.local/bin/mise" ]; then
  eval "$("$HOME/.local/bin/mise" activate zsh)"
fi

# macOS-isms used in aliases and fzf bindings
if command -v wl-copy > /dev/null; then
  export CLIPCOPY="wl-copy"
elif command -v xclip > /dev/null; then
  export CLIPCOPY="xclip -selection clipboard"
else
  export CLIPCOPY="cat > /dev/null"
fi
export LS_PREVIEW="ls -1 --color=always"
command -v open > /dev/null || alias open='xdg-open'
command -v pbcopy > /dev/null || alias pbcopy="$CLIPCOPY"
