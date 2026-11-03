# RVM settings
if [[ -s ~/.rvm/scripts/rvm ]] ; then
  RPS1="%{$fg[yellow]%}rvm:%{$reset_color%}%{$fg[red]%}\$(~/.rvm/bin/rvm-prompt)%{$reset_color%} $EPS1"
else
  if which rbenv &> /dev/null; then
    RPS1="%{$fg[yellow]%}rbenv:%{$reset_color%}%{$fg[red]%}\$(rbenv version | sed -e 's/ (set.*$//')%{$reset_color%} $EPS1"
  fi
fi

ZSH_THEME_GIT_PROMPT_PREFIX="%{$reset_color%}%{$fg[green]%}["
ZSH_THEME_GIT_PROMPT_SUFFIX="]%{$reset_color%}"
ZSH_THEME_GIT_PROMPT_DIRTY="%{$fg[red]%}*%{$reset_color%}"
ZSH_THEME_GIT_PROMPT_CLEAN=""

# ------------------------------------------------------------------------------
# VCS segment: [bookmark]* in a jj repo, [branch]* in a plain git repo.
#
# Everything runs in a background process and the prompt is redrawn in place
# when the result changes, so the prompt itself is never blocked:
#   - after every command (precmd)         -> instant prompt, segment lands ~25ms later
#   - on cd into a different directory     -> computed synchronously so the segment
#                                             is never briefly wrong for another repo
#   - every $VCS_STATUS_REFRESH s while idle, only inside a repo -> picks up edits
#     made in other panes / editors / Claude Code without pressing Enter
# Cost: one `jj log` process per check in a jj repo (no `jj root` pre-check),
# two git processes in a plain git repo, nothing at all outside a repo.
# ------------------------------------------------------------------------------

: ${VCS_STATUS_REFRESH:=3}

# Prints "<kind>\n<segment>" where kind is j (jj), g (git) or - (no repo).
# Safe to run in a subshell.
_vcs_status_compute() {
  local out
  # One jj call gives both @ (dirty flag) and the nearest bookmarked ancestor.
  # Each line is "<*-if-@-nonempty>|<bookmark names>"; @ is the only row whose
  # first field can be "*", and it may also be the bookmark row.
  # No --ignore-working-copy: this snapshots so `empty` reflects on-disk edits.
  if out=$(command jj log --no-graph --color never \
        -r '@ | latest(::@ & (bookmarks() | remote_bookmarks()))' \
        -T 'if(current_working_copy, if(empty, "", "*")) ++ "|" ++ if(local_bookmarks, local_bookmarks.map(|b| b.name()).join(","), remote_bookmarks.map(|b| b.name()).join(",")) ++ "\n"' \
        2> /dev/null); then
    local line dirty='' bookmark=''
    for line in ${(f)out}; do
      [[ $line == \** ]] && dirty='*'
      [[ -z $bookmark ]] && bookmark=${line#*|}
    done
    print -r -- j
    [[ -n $bookmark ]] && print -r -- "%{$fg[green]%}[${bookmark:gs/%/%%}]%{$reset_color%}%{$fg[red]%}$dirty%{$reset_color%}"
    return 0
  fi

  local cb=$(git_current_branch)
  if [[ -n $cb ]]; then
    print -r -- g
    print -r -- "$ZSH_THEME_GIT_PROMPT_PREFIX${cb:gs/%/%%}$ZSH_THEME_GIT_PROMPT_SUFFIX$(parse_git_dirty)"
    return 0
  fi
  print -r -- -
}

typeset -g _vcs_status=''       # what the prompt displays
typeset -g _vcs_status_kind='-' # j / g / - for the current directory
typeset -g _vcs_status_fd=''    # fd of an in-flight background check, if any
typeset -g _vcs_status_pwd=''   # directory the last check ran in

# Store a compute result and arm/disarm the idle timer accordingly.
_vcs_status_apply() {
  local kind=${1%%$'\n'*} seg=''
  [[ $1 == *$'\n'* ]] && seg=${1#*$'\n'}
  _vcs_status_kind=$kind
  # Idle alarm only inside a repo; TMOUT=0 disables it entirely elsewhere.
  if [[ $kind == - ]]; then TMOUT=0; else TMOUT=$VCS_STATUS_REFRESH; fi
  if [[ $seg != $_vcs_status ]]; then
    _vcs_status=$seg
    return 0        # changed
  fi
  return 1          # unchanged
}

_vcs_status_cancel_async() {
  [[ -n $_vcs_status_fd ]] || return 0
  zle -F $_vcs_status_fd 2> /dev/null
  exec {_vcs_status_fd}<&-
  _vcs_status_fd=''
}

# Kick off a background check; the result arrives via _vcs_status_on_ready.
_vcs_status_refresh_async() {
  [[ -n $_vcs_status_fd ]] && return 0   # one at a time
  exec {_vcs_status_fd}< <(_vcs_status_compute)
  zle -F $_vcs_status_fd _vcs_status_on_ready
}

# zle -F callback: fd is readable (or hit EOF).
_vcs_status_on_ready() {
  local fd=$1 out
  zle -F $fd
  out=$(<&$fd)
  exec {fd}<&-
  _vcs_status_fd=''
  if _vcs_status_apply "$out"; then
    zle && zle reset-prompt
  fi
}

_vcs_status_precmd() {
  _vcs_status_cancel_async
  if [[ $PWD != $_vcs_status_pwd ]]; then
    # New directory: block briefly (~25ms) rather than flash another repo's state.
    _vcs_status_pwd=$PWD
    _vcs_status_apply "$(_vcs_status_compute)"
  else
    # Same directory: draw the prompt now with the last value, correct it async.
    _vcs_status_refresh_async
  fi
}
autoload -Uz add-zsh-hook
add-zsh-hook precmd _vcs_status_precmd

# While the prompt is idle, zle raises ALRM every $TMOUT seconds.
TRAPALRM() {
  zle || return 0            # only while the line editor is waiting for input
  _vcs_status_refresh_async
}

PROMPT='%{$fg[cyan]%}[%~% ]${_vcs_status}%{$reset_color%}%B$%b '
