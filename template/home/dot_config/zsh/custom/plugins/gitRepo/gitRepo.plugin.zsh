# gho  - open the current repo/branch on GitHub (origin; `gho <anything>` uses the fork remote,
#        or that remote if the argument names one)
# ghc  - copy a GitHub link to a file (optionally :LINE or :START-END) to the clipboard

_gitrepo_slug() {
  local remote=${1:-origin}
  git rev-parse --show-toplevel >/dev/null 2>&1 || {
    print -u2 "Not a git repository"; return 1
  }

  local url
  url=$(git config --get "remote.$remote.url") && [[ -n "$url" ]] || {
    print -u2 "Could not find URL for remote: $remote"; return 1
  }
  [[ "$url" == *github.com* ]] || {
    print -u2 "Not a GitHub URL: $url"; return 1
  }

  # Works for both SSH and HTTPS remotes
  print -r -- "$(sed -E 's|.*github\.com[:/]||; s|\.git$||' <<< "$url")"
}

# The ref to link to: the current bookmark/branch when it exists on the remote,
# otherwise the default branch — an unpushed branch has no page on GitHub.
_gitrepo_branch() {
  local remote=${1:-origin} branch=""

  # Prefer the current jj bookmark (nearest bookmark reachable from @);
  # fall back to the current git branch when not in a jj repo.
  if jj root >/dev/null 2>&1; then
    # b.name() strips the @remote suffix of remote-tracking bookmarks.
    branch=$(jj log --no-graph --color never \
      -r 'heads(::@ & (bookmarks() | remote_bookmarks()))' \
      -T 'bookmarks.map(|b| b.name()).join("\n")' 2>/dev/null | head -n1)
  fi
  [[ -n "$branch" ]] || branch=$(git branch --show-current)

  if [[ -n "$branch" ]] && git rev-parse --verify --quiet "refs/remotes/$remote/$branch" >/dev/null; then
    print -r -- "$branch"
    return
  fi

  local default
  default=$(git symbolic-ref --short --quiet "refs/remotes/$remote/HEAD") ||
    default=$(git remote show "$remote" 2>/dev/null | sed -n 's/.*HEAD branch: //p')
  [[ -n "$default" ]] || {
    print -u2 "Could not determine default branch"; return 1
  }
  print -r -- "${default#$remote/}"
}

_gitrepo_open() {
  case "$(uname -s)" in
    Darwin*) open "$1" ;;
    Linux*)  xdg-open "$1" ;;
    CYGWIN*|MINGW*|MSYS*) start "$1" ;;
    *) print -u2 "Unsupported OS. Open manually: $1"; return 1 ;;
  esac
}

_gitrepo_copy() {
  print -rn -- "$1" | clipcopy 2>/dev/null && return

  # No clipboard tool installed: ask the terminal to do it (OSC 52).
  local b64=$(print -rn -- "$1" | base64 | tr -d '\n')
  if [[ -n "$TMUX" ]]; then
    printf '\ePtmux;\e\e]52;c;%s\a\e\\' "$b64" > /dev/tty
  else
    printf '\e]52;c;%s\a' "$b64" > /dev/tty
  fi
}

gho() {
  local remote=origin slug branch
  if (( $# )); then
    git remote get-url "$1" >/dev/null 2>&1 && remote=$1 || remote=fork
  fi
  slug=$(_gitrepo_slug "$remote") || return 1
  branch=$(_gitrepo_branch "$remote") || return 1
  _gitrepo_open "https://github.com/$slug/tree/$branch"
}

ghc() {
  local target=${1:?usage: ghc <file>[:LINE[-END]]}
  local file=${target%%:*} lines=""
  [[ "$target" == *:* ]] && lines="#L${${target#*:}/-/-L}"

  [[ -e "$file" ]] || { print -u2 "No such file: $file"; return 1 }

  local root slug branch
  root=$(git rev-parse --show-toplevel) || { print -u2 "Not a git repository"; return 1 }
  local rel=${${file:A}#${root:A}/}
  [[ "$rel" != /* ]] || { print -u2 "Not inside $root: $file"; return 1 }

  slug=$(_gitrepo_slug) || return 1
  branch=$(_gitrepo_branch) || return 1

  local kind=blob
  [[ -d "$file" ]] && kind=tree

  local url="https://github.com/$slug/$kind/$branch/$rel$lines"
  _gitrepo_copy "$url"
}

compdef _files ghc
