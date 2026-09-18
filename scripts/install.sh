#!/bin/sh
# Forcefield installer for Linux and macOS
# Repository: https://github.com/fabledruns/forcefield
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh -s -- --version v1.0.0
#   FORCEFIELD_VERSION=v1.0.0 sh install.sh
#   sh install.sh --version v1.0.0 --dir "$HOME/.local/bin"
set -eu

REPO="fabledruns/forcefield"
BINARY="ff"

# Defaults (overridable via args / env)
VERSION="${FORCEFIELD_VERSION:-}"
INSTALL_DIR="${FORCEFIELD_INSTALL_DIR:-}"
NO_MODIFY_PATH="${FORCEFIELD_NO_MODIFY_PATH:-0}"
FORCE="${FORCEFIELD_FORCE:-0}"

# ---------- helpers ----------

die() {
  # Fatal error, stderr: ✕ first line, indented continuation, exit 1.
  # The message text is preserved verbatim for diagnostics.
  text="$*"
  nl='
'
  first="${text%%"$nl"*}"
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s%s %s%s\n' "$C_ERROR" "$G_FAIL" "$first" "$C_RESET" >&2
  else
    printf '%s %s\n' "$G_FAIL" "$first" >&2
  fi
  if [ "$text" != "$first" ]; then
    rest="${text#*"$nl"}"
    printf '  %s\n' "$rest" >&2
  fi
  exit 1
}

usage() {
  cat <<'EOF'
Forcefield installer

Usage:
  install.sh [options]

Options:
  --version <tag>   Install specific version (e.g. v1.0.0). Default: latest release.
  --dir <path>      Install directory. Default: $HOME/.local/bin
  --no-modify-path  Do not try to add install dir to PATH
  --force           Reinstall even when the requested version is already installed
  -h, --help        Show this help

Environment:
  FORCEFIELD_VERSION      Same as --version
  FORCEFIELD_INSTALL_DIR  Same as --dir
  FORCEFIELD_FORCE        Same as --force (set to 1)
  NO_COLOR                Set (to anything) for plain, log-friendly output
  FORCEFIELD_ASCII        Set to 1 for ASCII output on a terminal
EOF
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    return 1
  fi
  return 0
}

is_in_path() {
  # POSIX check: ":$PATH:" contains ":$1:"
  case ":${PATH:-}:" in
    *":$1:"*) return 0 ;;
    *) return 1 ;;
  esac
}

# The user-local default install directory. Prints "$HOME/.local/bin";
# fails when HOME is unset so callers never install to a garbage path.
default_install_dir() {
  if [ -z "${HOME:-}" ]; then
    return 1
  fi
  printf "%s" "$HOME/.local/bin"
}

# Expand a leading ~ or ~/ to $HOME. Anything else passes through.
expand_tilde() {
  case "$1" in
    "~") printf "%s" "${HOME:-~}" ;;
    "~/"*) printf "%s" "${HOME:-}${1#\~}" ;;
    *) printf "%s" "$1" ;;
  esac
}

# Strip trailing slashes ("/opt/ff/" -> "/opt/ff"), keeping "/" intact.
normalize_dir() {
  d="$1"
  while [ "$d" != "/" ]; do
    case "$d" in
      */) d="${d%/}" ;;
      *) break ;;
    esac
  done
  printf "%s" "$d"
}

# Decide the terminal output mode. Prints "<color|plain>+<unicode|ascii>":
# color only on a real TTY without NO_COLOR and with a capable TERM;
# unicode only with UTF-8 evidence (or forced off via FORCEFIELD_ASCII).
# FORCEFIELD_TTY=0/1 overrides TTY probing (useful for tests and CI).
# No cursor movement, no animation, no width dependence.
detect_ui_mode() {
  tty="0"
  if [ "${FORCEFIELD_TTY:-}" = "1" ]; then
    tty="1"
  elif [ "${FORCEFIELD_TTY:-}" = "0" ]; then
    tty="0"
  elif [ -t 1 ]; then
    tty="1"
  fi

  color="color"
  if [ "${FORCEFIELD_NO_COLOR+set}" = "set" ] || [ "${NO_COLOR+set}" = "set" ]; then
    color="plain"
  fi
  # An empty TERM means "unknown", not "dumb": stay plain unless another
  # modern-terminal signal (COLORTERM) vouches for color support.
  case "${TERM:-}" in
    dumb) color="plain" ;;
    "")
      case "${COLORTERM:-}" in
        "") color="plain" ;;
      esac
      ;;
  esac
  if [ "$tty" != "1" ]; then
    color="plain"
  fi

  glyphs="ascii"
  loc="${LC_ALL:-${LC_CTYPE:-${LANG:-}}}"
  case "$loc" in
    *[Uu][Tt][Ff]-8*|*[Uu][Tt][Ff]8*) glyphs="unicode" ;;
  esac
  if [ "${FORCEFIELD_ASCII:-0}" = "1" ]; then
    glyphs="ascii"
  fi

  printf "%s+%s" "$color" "$glyphs"
}

# ---------- terminal UI (presentation only) ----------
# Rendering helpers. Behavior lives elsewhere; these only format text.
# UI_COLOR/UI_GLYPHS come from ui_init (explicit mode or detect_ui_mode).
# The palette mirrors internal/tui/styles.go: accent #ED2663,
# text #EAEAEA, muted #7A7A7A, error #FF6B6B, success #7D9B76,
# warning #C4A35A. Glyphs mirror internal/tui/icons.go.

UI_COLOR="plain"
UI_GLYPHS="ascii"
C_ACCENT=""; C_TEXT=""; C_MUTED=""; C_ERROR=""; C_SUCCESS=""; C_WARN=""; C_RESET=""
C_BOLD=""; C_BOLD_ACCENT=""
G_OK="*"; G_STEP=">"; G_PEND="o"; G_FAIL="x"; G_WARN="!"; G_ARROW="->"
UI_RULE_LEN=40

ui_init() {
  # ui_init [mode]: adopt $1, defaulting to detect_ui_mode, then rebuild
  # the palette and glyphs. Call once before any output.
  mode="${1:-$(detect_ui_mode)}"
  case "$mode" in
    color+*) UI_COLOR="color" ;;
    *) UI_COLOR="plain" ;;
  esac
  case "$mode" in
    *+unicode) UI_GLYPHS="unicode" ;;
    *) UI_GLYPHS="ascii" ;;
  esac
  ui_setup_palette
  ui_setup_glyphs
}

ui_setup_palette() {
  # Truecolor codes when COLORTERM says so, 256-color approximations of
  # the TUI palette on 256-color terminals, basic red/green/yellow
  # otherwise. Plain mode leaves every code empty (never emit ANSI).
  C_ACCENT=""; C_TEXT=""; C_MUTED=""; C_ERROR=""; C_SUCCESS=""; C_WARN=""; C_RESET=""
  C_BOLD=""; C_BOLD_ACCENT=""
  if [ "$UI_COLOR" != "color" ]; then
    return 0
  fi
  depth="basic"
  case "${COLORTERM:-}" in
    *truecolor*|*24bit*) depth="true" ;;
    *)
      case "${TERM:-}" in
        *256color*) depth="256" ;;
      esac
      ;;
  esac
  if [ "$depth" = "true" ]; then
    C_ACCENT="$(printf '\033[38;2;237;38;99m')"
    C_TEXT="$(printf '\033[38;2;234;234;234m')"
    C_MUTED="$(printf '\033[38;2;122;122;122m')"
    C_ERROR="$(printf '\033[38;2;255;107;107m')"
    C_SUCCESS="$(printf '\033[38;2;125;155;118m')"
    C_WARN="$(printf '\033[38;2;196;163;90m')"
    C_RESET="$(printf '\033[0m')"
    C_BOLD="$(printf '\033[1m')"
    C_BOLD_ACCENT="$(printf '\033[1;38;2;237;38;99m')"
  elif [ "$depth" = "256" ]; then
    C_ACCENT="$(printf '\033[38;5;197m')"
    C_TEXT="$(printf '\033[38;5;254m')"
    C_MUTED="$(printf '\033[38;5;243m')"
    C_ERROR="$(printf '\033[38;5;203m')"
    C_SUCCESS="$(printf '\033[38;5;108m')"
    C_WARN="$(printf '\033[38;5;179m')"
    C_RESET="$(printf '\033[0m')"
    C_BOLD="$(printf '\033[1m')"
    C_BOLD_ACCENT="$(printf '\033[1;38;5;197m')"
  else
    C_ACCENT="$(printf '\033[31m')"
    C_ERROR="$(printf '\033[31m')"
    C_SUCCESS="$(printf '\033[32m')"
    C_WARN="$(printf '\033[33m')"
    C_RESET="$(printf '\033[0m')"
    C_BOLD="$(printf '\033[1m')"
    C_BOLD_ACCENT="$(printf '\033[1;31m')"
  fi
}

ui_setup_glyphs() {
  # Shared Forcefield vocabulary (see internal/tui/icons.go). ASCII
  # fallbacks keep piped logs and non-UTF-8 terminals readable.
  if [ "$UI_GLYPHS" = "unicode" ]; then
    G_OK="✓"; G_STEP="›"; G_PEND="○"; G_FAIL="✕"; G_WARN="!"; G_ARROW="→"
  else
    G_OK="*"; G_STEP=">"; G_PEND="o"; G_FAIL="x"; G_WARN="!"; G_ARROW="->"
  fi
}

brand() {
  # Compact Forcefield brand: FORCE in the accent, FIELD in white, plus
  # the tagline. Plain mode prints the same words without decoration.
  if [ "$UI_COLOR" = "color" ]; then
    printf '%sFORCE%s%sFIELD%s\n' "$C_BOLD_ACCENT" "$C_RESET" "$C_BOLD" "$C_RESET"
    printf '%sthe local-first agent harness%s\n' "$C_MUTED" "$C_RESET"
  else
    printf 'FORCEFIELD\nthe local-first agent harness\n'
  fi
}

ui_rule() {
  # Thin fixed-width separator. No width probing: fixed length keeps
  # piped output deterministic.
  if [ "$UI_GLYPHS" = "unicode" ]; then ch="─"; else ch="-"; fi
  line=""
  i=0
  while [ "$i" -lt "$UI_RULE_LEN" ]; do line="$line$ch"; i=$((i + 1)); done
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s%s%s\n' "$C_MUTED" "$line" "$C_RESET"
  else
    printf '%s\n' "$line"
  fi
}

ui_step() {
  # Active step, stdout: › Label
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s%s%s %s\n' "$C_ACCENT" "$G_STEP" "$C_RESET" "$*"
  else
    printf '%s %s\n' "$G_STEP" "$*"
  fi
}

ui_done() {
  # Completed step, stdout: ✓ Label
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s%s%s %s\n' "$C_SUCCESS" "$G_OK" "$C_RESET" "$*"
  else
    printf '%s %s\n' "$G_OK" "$*"
  fi
}

ui_warn() {
  # Recoverable warning, stderr: ! message
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s%s%s %s\n' "$C_WARN" "$G_WARN" "$C_RESET" "$*" >&2
  else
    printf '%s %s\n' "$G_WARN" "$*" >&2
  fi
}

ui_row() {
  # Aligned label/value detail, stdout: two-space indent, padded label.
  printf '  %-12s %s\n' "$1" "$2"
}

ui_detail() {
  # Muted detail line, stdout.
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s  %s%s\n' "$C_MUTED" "$*" "$C_RESET"
  else
    printf '  %s\n' "$*"
  fi
}

# Strip the leading v/V prefix and any +build metadata, so "v1.2.0-rc.1+001"
# becomes "1.2.0-rc.1". Pure string handling; validation stays in
# validate_version.
normalize_version() {
  v="$1"
  case "$v" in
    [vV]*) v="${v#?}" ;;
  esac
  case "$v" in
    *"+"*) v="${v%%+*}" ;;
  esac
  printf "%s" "$v"
}

# Compare two versions after normalization. Prints exactly one of -1, 0, 1
# (a older, equal, a newer). Numeric fields compare numerically ("1.2.10"
# beats "1.2.3"); missing fields count as zero; a release beats its own
# prerelease ("1.2.3" beats "1.2.3-rc.1"); prerelease fields compare
# numerically when both are numeric, lexically otherwise.
compare_versions() {
  a="$(normalize_version "$1")"
  b="$(normalize_version "$2")"
  a_base="${a%%-*}"
  b_base="${b%%-*}"
  if [ "$a" = "$a_base" ]; then a_pre=""; else a_pre="${a#*-}"; fi
  if [ "$b" = "$b_base" ]; then b_pre=""; else b_pre="${b#*-}"; fi

  cmp_fields() {
    x="$1"
    y="$2"
    while [ -n "$x" ] || [ -n "$y" ]; do
      case "$x" in
        *"."*) xf="${x%%.*}"; x="${x#*.}" ;;
        *) xf="$x"; x="" ;;
      esac
      case "$y" in
        *"."*) yf="${y%%.*}"; y="${y#*.}" ;;
        *) yf="$y"; y="" ;;
      esac
      if [ -z "$xf" ]; then xf="0"; fi
      if [ -z "$yf" ]; then yf="0"; fi
      case "$xf$yf" in
        *[!0-9]*)
          if [ "$xf" '<' "$yf" ]; then printf "%s" "-1"; return 0; fi
          if [ "$xf" '>' "$yf" ]; then printf "%s" "1"; return 0; fi
          ;;
        *)
          if [ "$xf" -lt "$yf" ]; then printf "%s" "-1"; return 0; fi
          if [ "$xf" -gt "$yf" ]; then printf "%s" "1"; return 0; fi
          ;;
      esac
    done
    printf "%s" "0"
  }

  r="$(cmp_fields "$a_base" "$b_base")"
  if [ "$r" != "0" ]; then printf "%s" "$r"; return 0; fi
  if [ "$a_pre" = "$b_pre" ]; then printf "%s" "0"; return 0; fi
  if [ -z "$a_pre" ]; then printf "%s" "1"; return 0; fi
  if [ -z "$b_pre" ]; then printf "%s" "-1"; return 0; fi
  cmp_fields "$a_pre" "$b_pre"
}

# Read the installed version from an existing binary by running
# "<bin> --version" and parsing the "ff version <tag>" line. Prints the
# tag (e.g. "v1.2.3", or "dev" for local builds) and returns 0, or prints
# nothing and returns 1 when the binary is missing, unrunnable, or its
# output is unrecognizable. Never fails the caller: detection problems
# mean "unknown", never an install error.
installed_version_of() {
  bin="$1"
  if [ ! -f "$bin" ]; then
    return 1
  fi
  out="$("$bin" --version 2>/dev/null || true)"
  ver="$(printf "%s" "$out" | head -n 1 | awk '{print $3}')"
  if [ -z "$ver" ]; then
    return 1
  fi
  case "$ver" in
    *[!A-Za-z0-9._+\-]*)
      return 1
      ;;
  esac
  printf "%s" "$ver"
}

# Decide what an install run should do. Prints exactly one of:
# fresh (nothing installed), upgrade (installed older or unknown),
# current (installed equals target), reinstall (equal/newer plus force),
# refuse-downgrade (installed newer, no force).
# $3 force is "1" or anything else; $4 exists is "1" or anything else.
decide_install_action() {
  installed="$1"
  target="$2"
  force="$3"
  exists="$4"
  if [ "$exists" != "1" ]; then
    printf "%s" "fresh"
    return 0
  fi
  case "$installed" in
    [vV][0-9]*|[0-9]*)
      ;;
    *)
      # Unknown version (dev builds, unparsable output): today's
      # behavior overwrites in place, so treat as upgrade.
      printf "%s" "upgrade"
      return 0
      ;;
  esac
  cmp="$(compare_versions "$installed" "$target")"
  case "$cmp" in
    -1)
      printf "%s" "upgrade"
      ;;
    0)
      if [ "$force" = "1" ]; then printf "%s" "reinstall"; else printf "%s" "current"; fi
      ;;
    1)
      if [ "$force" = "1" ]; then printf "%s" "reinstall"; else printf "%s" "refuse-downgrade"; fi
      ;;
  esac
}

# Detect the existing installation (if any) and decide the action for
# target $1 with force $2 ("1" or "0") in directory $3. Prints the action
# from decide_install_action; detection never fails the caller.
resolve_install_action() {
  target="$1"
  force="$2"
  dir="$3"
  bin="$dir/$BINARY"
  exists="0"
  if [ -f "$bin" ]; then
    exists="1"
  fi
  installed="$(installed_version_of "$bin" || true)"
  decide_install_action "$installed" "$target" "$force" "$exists"
}

# Validate version string to prevent injection / traversal.
# Allowed: v1.2.3, 1.2.3, v1.0.0-rc.1, v1.2.3-alpha+001 etc  (alnum . _ - +)
validate_version() {
  case "$1" in
    "" ) return 0 ;;
    v[0-9]*|V[0-9]*|[0-9]* ) ;;
    *) die "invalid version format: $1 (expected vX.Y.Z)" ;;
  esac
  # shellcheck: ensure no path traversal or shell metachars
  case "$1" in
    *".."* | *"/"* | *"\\"* | *";"* | *"&"* | *"|"* | *"\`"* | *"\$"* | *" "* | *"'\""* )
      die "invalid version (contains forbidden characters): $1"
      ;;
  esac
  # only allow known chars
  # Use case + trim: if version contains anything outside [A-Za-z0-9._v+-], reject
  # POSIX: use printf + grep
  if printf "%s" "$1" | grep -qE '[^A-Za-z0-9._+\-v]'; then
    # grep -E may not be available everywhere, fallback to sed
    # If grep fails, second check via tr
    if printf "%s" "$1" | tr -d 'A-Za-z0-9._+\-v' | grep -q .; then
      die "invalid version (unsupported characters): $1"
    fi
  fi
}

# Parse installer arguments into VERSION, INSTALL_DIR, NO_MODIFY_PATH,
# FORCE. Exits (via die/usage) on bad input, like the flow always did.
parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --version|-v)
        if [ $# -lt 2 ]; then die "--version requires an argument"; fi
        VERSION="$2"
        if [ -z "$VERSION" ]; then die "--version requires non-empty argument"; fi
        shift 2
        ;;
      --version=*)
        VERSION="${1#--version=}"
        if [ -z "$VERSION" ]; then die "--version requires non-empty argument"; fi
        shift
        ;;
      --dir|--install-dir)
        if [ $# -lt 2 ]; then die "--dir requires an argument"; fi
        INSTALL_DIR="$2"
        if [ -z "$INSTALL_DIR" ]; then die "--dir requires non-empty argument"; fi
        shift 2
        ;;
      --dir=*)
        INSTALL_DIR="${1#--dir=*}"
        INSTALL_DIR="${1#--dir=}"
        if [ -z "$INSTALL_DIR" ]; then die "--dir requires non-empty argument"; fi
        shift
        ;;
      --no-modify-path)
        NO_MODIFY_PATH=1
        shift
        ;;
      --force)
        FORCE=1
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      --)
        shift
        break
        ;;
      -*)
        die "unknown option: $1 (use --help)"
        ;;
      *)
        die "unexpected argument: $1 (use --help)"
        ;;
    esac
  done
}

detect_os() {
  os="$(uname -s 2>/dev/null || echo unknown)"
  case "$os" in
    Linux)  printf "linux" ;;
    Darwin) printf "darwin" ;;
    MINGW*|MSYS*|CYGWIN*|Windows_NT)
      die "Windows detected. Use the PowerShell installer instead:

  irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex

Or download the binary manually from https://github.com/${REPO}/releases"
      ;;
    *)
      die "unsupported OS: $os
Supported: Linux, macOS.
Download a binary manually: https://github.com/${REPO}/releases"
      ;;
  esac
}

detect_arch() {
  arch="$(uname -m 2>/dev/null || echo unknown)"
  case "$arch" in
    x86_64|amd64)  printf "amd64" ;;
    arm64|aarch64) printf "arm64" ;;
    *)
      die "unsupported architecture: $arch (uname -m)
Supported: amd64 (x86_64), arm64 (aarch64).
You can download a binary manually: https://github.com/${REPO}/releases"
      ;;
  esac
}

fetch_latest_version() {
  api_latest="https://api.github.com/repos/${REPO}/releases/latest"
  api_list="https://api.github.com/repos/${REPO}/releases?per_page=20"
  tag=""

  # Helper: extract first stable tag from a releases list JSON, fallback to first tag
  extract_tag() {
    json="$1"
    # Try to find first stable (prerelease == false)
    stable="$(printf "%s" "$json" | tr '}' '\n' | while IFS= read -r chunk; do
      if printf "%s" "$chunk" | grep -q '"prerelease"[[:space:]]*:[[:space:]]*false'; then
        t="$(printf "%s" "$chunk" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 | cut -d'"' -f4 || true)"
        if [ -n "$t" ]; then printf "%s" "$t"; break; fi
      fi
    done)"
    if [ -n "$stable" ]; then
      printf "%s" "$stable"
      return
    fi
    # No stable found, return first tag (likely prerelease)
    printf "%s" "$json" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 | cut -d'"' -f4 || true
  }

  # Prefer curl, fallback to wget
  if need_cmd curl; then
    # Try /releases/latest first (works for stable releases)
    resp="$(curl -fsSL -H "Accept: application/vnd.github.v3+json" -H "User-Agent: forcefield-installer" "$api_latest" 2>/dev/null || true)"
    tag="$(printf "%s" "$resp" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 | cut -d'"' -f4 || true)"
    if [ -z "$tag" ]; then
      resp="$(curl -fsSL -H "Accept: application/vnd.github.v3+json" -H "User-Agent: forcefield-installer" "$api_list" 2>/dev/null || true)"
      tag="$(extract_tag "$resp")"
    fi
  elif need_cmd wget; then
    resp="$(wget -qO- --header="Accept: application/vnd.github.v3+json" --header="User-Agent: forcefield-installer" "$api_latest" 2>/dev/null || true)"
    tag="$(printf "%s" "$resp" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 | cut -d'"' -f4 || true)"
    if [ -z "$tag" ]; then
      resp="$(wget -qO- --header="Accept: application/vnd.github.v3+json" --header="User-Agent: forcefield-installer" "$api_list" 2>/dev/null || true)"
      tag="$(extract_tag "$resp")"
    fi
  else
    die "need curl or wget to determine latest version. Install one or pass --version vX.Y.Z"
  fi

  if [ -z "$tag" ]; then
    die "could not determine latest release. GitHub API may be rate-limited or offline.
Try: install.sh --version v1.0.0
Or download manually: https://github.com/${REPO}/releases"
  fi
  printf "%s" "$tag"
}

download_file() {
  # $1 = url, $2 = dest. Quiet on success (no progress meters); errors
  # still surface via -sS / -q semantics and the caller's diagnostics.
  url="$1"
  dest="$2"
  if need_cmd curl; then
    curl -fsSL -sS -o "$dest" "$url"
  elif need_cmd wget; then
    wget -qO "$dest" "$url"
  else
    die "need curl or wget to download $url"
  fi
}

sha256_of_file() {
  file="$1"
  if need_cmd sha256sum; then
    sha256sum "$file" | awk '{print $1}'
  elif need_cmd shasum; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif need_cmd openssl; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    ui_warn "no sha256 tool found (sha256sum/shasum/openssl); skipping checksum verification"
    return 1
  fi
}

# ---------- main ----------
# The installer flow lives in main() so the file can also be sourced
# with FORCEFIELD_LIB_ONLY=1 for testing: sourcing loads every helper
# without executing anything (no network, no filesystem changes).
main() {
# ---------- argument parsing ----------
parse_args "$@"

# Initialize terminal rendering once (mode detection is TTY-aware and
# honors NO_COLOR/TERM; --help above exits before any output).
ui_init

# ---------- resolve install dir ----------
if [ -z "${HOME:-}" ]; then
  die "HOME is not set; cannot determine install directory. Pass --dir <path>."
fi

if [ -z "$INSTALL_DIR" ]; then
  INSTALL_DIR="$HOME/.local/bin"
fi

# Expand leading ~/ and ~/
case "$INSTALL_DIR" in
  "~") INSTALL_DIR="$HOME" ;;
  "~/"*) INSTALL_DIR="$HOME/${INSTALL_DIR#\~/}" ;;
esac

# Reject empty, and for safety reject paths that look like repo/config dirs
# (we never want to install into ~/.forcefield or current directory silently)
case "$INSTALL_DIR" in
  "") die "install dir is empty" ;;
esac

# Ensure absolute path (or at least contains / and not weird)
case "$INSTALL_DIR" in
  /*) ;;
  *)
    # Allow relative? For safety, require absolute or HOME-relative which we already handled.
    # Convert relative to absolute via current dir
    ui_warn "install dir is not absolute: $INSTALL_DIR (resolving relative to current directory)"
    INSTALL_DIR="$(pwd)/$INSTALL_DIR"
    ;;
esac

# Validate no PATH delimiter or control characters in install dir
case "$INSTALL_DIR" in
  *:* ) die "install dir contains ':' which is the PATH delimiter: $INSTALL_DIR" ;;
  *";"* ) die "install dir contains ';' which is the PATH delimiter: $INSTALL_DIR" ;;
esac
if [ "$INSTALL_DIR" != "$(printf "%s" "$INSTALL_DIR" | tr -d '\n\r\t' 2>/dev/null || printf "%s" "$INSTALL_DIR")" ]; then
  die "install dir contains newline, carriage return, or tab: $INSTALL_DIR"
fi

validate_version "$VERSION"

brand
ui_rule
printf "\n"

# ---------- detect platform ----------
OS="$(detect_os)"
ARCH="$(detect_arch)"
ui_step "Detecting platform"
ui_row "Platform" "${OS}/${ARCH}"

# ---------- resolve version ----------
if [ -z "$VERSION" ]; then
  ui_step "Resolving latest release"
  VERSION="$(fetch_latest_version)"
  ui_done "Release $VERSION"
else
  # normalize: ensure v prefix
  case "$VERSION" in
    v*) ;;
    *) VERSION="v$VERSION" ;;
  esac
fi

validate_version "$VERSION"

ARTIFACT="ff-${OS}-${ARCH}"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARTIFACT}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"
# Fallback alt name for old releases (checksums.txt vs SHA256SUMS) - we handle via try

# ---------- existing installation ----------
# Decide before any download: already-current installs exit early without
# touching the network artifact or the binary; downgrades refuse unless
# forced. Force only bypasses these two gates — verification, platform,
# path safety, and atomic replacement below always apply.
FORCE_FLAG="0"
if [ "$FORCE" = "1" ]; then
  FORCE_FLAG="1"
fi
INSTALLED_VER="$(installed_version_of "$INSTALL_DIR/$BINARY" || true)"
if [ -f "$INSTALL_DIR/$BINARY" ]; then
  ACTION="$(decide_install_action "$INSTALLED_VER" "$VERSION" "$FORCE_FLAG" "1")"
else
  ACTION="fresh"
fi
case "$ACTION" in
  current)
    ui_done "Forcefield $VERSION is already installed"
    ui_row "Location" "$INSTALL_DIR/$BINARY"
    if is_in_path "$INSTALL_DIR"; then
      ui_row "PATH" "configured"
    else
      ui_row "PATH" "missing"
      ui_warn "$INSTALL_DIR is not in PATH. Add it manually or re-run with --force to reinstall:"
      ui_detail "export PATH=\"$INSTALL_DIR:\$PATH\""
    fi
    exit 0
    ;;
  refuse-downgrade)
    die "installed version ${INSTALLED_VER:-unknown} is newer than $VERSION; refusing to downgrade. Pass --force to reinstall anyway."
    ;;
esac

# ---------- installation plan ----------
ui_step "Installation plan"
ui_row "Version" "$VERSION"
case "$ACTION" in
  fresh)
    ui_row "Action" "Fresh install"
    ;;
  upgrade)
    if [ -n "$INSTALLED_VER" ]; then
      ui_row "Action" "Upgrade $INSTALLED_VER $G_ARROW $VERSION"
    else
      ui_row "Action" "Upgrade"
    fi
    ;;
  reinstall)
    ui_row "Action" "Reinstall $VERSION"
    ;;
esac
ui_row "Location" "$INSTALL_DIR/$BINARY"
if is_in_path "$INSTALL_DIR"; then
  ui_row "PATH" "configured"
elif [ "$NO_MODIFY_PATH" = "1" ]; then
  ui_row "PATH" "will not be modified"
else
  ui_row "PATH" "will be configured"
fi

# ---------- temp dir ----------
if need_cmd mktemp; then
  TMPDIR_ROOT="${TMPDIR:-/tmp}"
  TMP="$(mktemp -d "${TMPDIR_ROOT}/forcefield-install.XXXXXX" 2>/dev/null || mktemp -d 2>/dev/null || echo "")"
  if [ -z "$TMP" ] || [ ! -d "$TMP" ]; then
    die "failed to create temp directory"
  fi
else
  die "mktemp is required but not found"
fi

# Ensure permissions 700 and cleanup
chmod 700 "$TMP" 2>/dev/null || true

cleanup() {
  rm -rf "$TMP" 2>/dev/null || true
}
trap cleanup EXIT INT TERM HUP

# ---------- download ----------
ui_step "Downloading $ARTIFACT $VERSION"
if ! download_file "$DOWNLOAD_URL" "$TMP/$ARTIFACT"; then
  die "Download failed: $DOWNLOAD_URL
The artifact may not exist for ${OS}/${ARCH} at $VERSION.
Check https://github.com/${REPO}/releases/tag/${VERSION} and supported architectures: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64"
fi

if [ ! -s "$TMP/$ARTIFACT" ]; then
  die "downloaded file is empty: $TMP/$ARTIFACT (check $DOWNLOAD_URL)"
fi
ui_done "Download complete"

# ---------- checksum ----------
ui_step "Verifying checksum"
CHECKSUM_FILE="$TMP/checksums.txt"
CHECKSUM_OK=0
CHECKSUM_TMP="$TMP/checksums.tmp"

# Try primary checksums.txt, then fallback to SHA256SUMS / checksums.txt variations
for url in "$CHECKSUM_URL" "https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS" "https://github.com/${REPO}/releases/download/${VERSION}/checksums.sha256"; do
  if download_file "$url" "$CHECKSUM_TMP" 2>/dev/null; then
    if [ -s "$CHECKSUM_TMP" ]; then
      mv "$CHECKSUM_TMP" "$CHECKSUM_FILE"
      ui_detail "Checksums: $url"
      break
    fi
  fi
  rm -f "$CHECKSUM_TMP" 2>/dev/null || true
done

if [ -f "$CHECKSUM_FILE" ]; then
  # Extract expected hash for our artifact; format is "<hash>  <filename>" (GNU) or "<hash> *<filename>" (binary)
  # Use exact filename match, not substring, to prevent similarly-named artifacts from satisfying verification.
  expected="$(awk -v art="$ARTIFACT" '{
    f=$2; sub(/^\*/, "", f); sub(/\r$/, "", f);
    if (f == art) { print $1; exit }
  }' "$CHECKSUM_FILE" 2>/dev/null || true)"
  # Fallback for CRLF or extra handling: also try stripping * and comparing
  if [ -z "$expected" ]; then
    expected="$(awk -v art="$ARTIFACT" '{
      for (i=2;i<=NF;i++) { f=$i; sub(/^\*/, "", f); sub(/\r$/, "", f); if (f == art) {print $1; exit}}
    }' "$CHECKSUM_FILE" 2>/dev/null || true)"
  fi
  if [ -z "$expected" ]; then
    ui_warn "checksums.txt does not contain $ARTIFACT; skipping verification (this should not happen for new releases)"
  else
    # Normalize to lowercase
    expected="$(printf "%s" "$expected" | tr 'A-F' 'a-f')"
    actual="$(sha256_of_file "$TMP/$ARTIFACT" 2>/dev/null || true)"
    if [ -z "$actual" ]; then
      ui_warn "could not compute sha256; skipping verification"
    else
      actual="$(printf "%s" "$actual" | tr 'A-F' 'a-f')"
      if [ "$expected" != "$actual" ]; then
        die "Checksum mismatch for $ARTIFACT
  expected: $expected
  actual:   $actual
Refusing to install. The download may be corrupted or tampered with."
      fi
      ui_done "Checksum verified"
      CHECKSUM_OK=1
    fi
  fi
else
  ui_warn "no checksums file at $CHECKSUM_URL"
  ui_warn "skipping verification (old releases before v1.2 may not have checksums)"
  ui_detail "for security, prefer a release with checksums.txt or verify manually"
fi

# ---------- install ----------
ui_step "Installing to $INSTALL_DIR"
mkdir -p "$INSTALL_DIR" || die "failed to create $INSTALL_DIR"

# Detect existing installation
EXISTING_BIN="$INSTALL_DIR/$BINARY"
if [ -f "$EXISTING_BIN" ]; then
  if [ -n "$INSTALLED_VER" ]; then
    ui_detail "Existing installation $INSTALLED_VER found (installing $VERSION)"
  else
    ui_detail "Existing installation found (upgrading in place)"
  fi
else
  ui_detail "No existing installation"
fi

# Never touch ~/.forcefield - explicitly ensure we are not writing there unless user chose it
# If install dir is inside ~/.forcefield, warn but still allow if explicitly requested?
case "$INSTALL_DIR" in
  "$HOME/.forcefield" | "$HOME/.forcefield"/*)
    ui_warn "install dir is inside ~/.forcefield; sessions/config live there. This is allowed but unusual."
    ;;
esac

# Atomic install: copy to temp file inside install dir, then rename atomically.
# Using a temp file in the same directory ensures the final rename is atomic
# on the same filesystem and avoids cross-device issues with /tmp on a different mount.
TMP_BIN="$INSTALL_DIR/.ff.tmp.$$"
# Ensure any stale temp from a previous interrupted run is removed first
rm -f "$TMP_BIN" 2>/dev/null || true
cp "$TMP/$ARTIFACT" "$TMP_BIN" || die "failed to stage binary to $TMP_BIN"
chmod +x "$TMP_BIN" || die "chmod +x failed"
# Extend cleanup to also remove the staging file in the install dir
cleanup() {
  rm -rf "$TMP" 2>/dev/null || true
  rm -f "$TMP_BIN" 2>/dev/null || true
}
# Re-install trap to include TMP_BIN (overwrites previous trap)
trap cleanup EXIT INT TERM HUP

# Move into place (atomic rename within same directory)
if mv -f "$TMP_BIN" "$EXISTING_BIN" 2>/dev/null; then
  :
else
  # Fallback if mv fails (e.g., permission or Windows-like locking on WSL)
  # Use cat to avoid truncating destination on failure
  if ! cat "$TMP_BIN" > "$EXISTING_BIN" 2>/dev/null; then
    rm -f "$TMP_BIN" 2>/dev/null || true
    die "failed to copy binary to $EXISTING_BIN"
  fi
  chmod +x "$EXISTING_BIN" || true
  rm -f "$TMP_BIN" 2>/dev/null || true
fi

# Ensure executable
chmod +x "$EXISTING_BIN" 2>/dev/null || true

ui_done "Installed $EXISTING_BIN"

# ---------- verify ----------
ui_step "Verifying installation"
if [ -x "$EXISTING_BIN" ]; then
  if "$EXISTING_BIN" --version >/dev/null 2>&1; then
    INSTALLED_VER="$("$EXISTING_BIN" --version 2>/dev/null | head -n 1 || true)"
    ui_done "Verified: $INSTALLED_VER"
  else
    # Try without --version (some older dev builds)
    ui_warn "installed binary does not support --version; checking help instead"
    if ! "$EXISTING_BIN" --help >/dev/null 2>&1; then
      ui_warn "installed binary failed to run --help; it may be the wrong architecture"
    fi
    INSTALLED_VER="(unknown, use ff --version after fixing PATH)"
  fi
else
  ui_warn "installed binary is not executable: $EXISTING_BIN"
fi

# ---------- PATH ----------
NEEDS_PATH_UPDATE=0
if is_in_path "$INSTALL_DIR"; then
  ui_done "PATH already contains $INSTALL_DIR"
else
  NEEDS_PATH_UPDATE=1
  if [ "$NO_MODIFY_PATH" = "1" ]; then
    ui_warn "PATH does not contain $INSTALL_DIR (path modification disabled via --no-modify-path)"
  else
    ui_step "Configuring PATH"

    # Determine which rc files to try
    # We try to be conservative: only modify files that already exist, plus one fallback.
    # Create list dynamically without bash arrays (POSIX)
    added=0

    # Function to add export line if not present
    add_path_to_file() {
      file="$1"
      line="$2"
      # Ensure file exists or we are willing to create it?
      # Only create if file is standard rc and parent dir exists
      dir="$(dirname "$file")"
      if [ ! -d "$dir" ]; then
        return 1
      fi
      # If file doesn't exist, create it (only for primary shells)
      if [ ! -f "$file" ]; then
        # Only create if it's expected rc file for this OS
        case "$file" in
          "$HOME/.bashrc"|"$HOME/.zshrc"|"$HOME/.profile")
            : # allow create
            ;;
          *)
            return 1
            ;;
        esac
      fi
      # Check if already contains install dir (avoid duplicate)
      if [ -f "$file" ] && grep -F -q "$INSTALL_DIR" "$file" 2>/dev/null; then
        ui_detail "$file already references $INSTALL_DIR (skipping)"
        added=1
        return 0
      fi
      # Append with a marker comment
      {
        echo ""
        echo "# Added by Forcefield installer ($(date -u +%Y-%m-%d 2>/dev/null || echo install))"
        echo "$line"
      } >> "$file" 2>/dev/null || return 1
      ui_detail "Updated $file"
      added=1
      return 0
    }

    # Bash / generic POSIX
    BASH_LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
    ZSH_LINE="export PATH=\"$INSTALL_DIR:\$PATH\""

    # Try to detect shell to prioritize, but also update common files
    shell_name="$(basename "${SHELL:-}" 2>/dev/null || echo "")"

    case "$shell_name" in
      *zsh)
        add_path_to_file "$HOME/.zshrc" "$ZSH_LINE" || true
        add_path_to_file "$HOME/.zprofile" "$ZSH_LINE" || true
        ;;
      *fish)
        # Quote the path for fish (handles spaces); fish_add_path is the idiomatic fish way to add to PATH
        FISH_LINE="fish_add_path \"$INSTALL_DIR\""
        add_path_to_file "$HOME/.config/fish/config.fish" "$FISH_LINE" || true
        ;;
      *bash|*)
        # For bash and unknown, try bashrc then profile
        add_path_to_file "$HOME/.bashrc" "$BASH_LINE" || true
        add_path_to_file "$HOME/.bash_profile" "$BASH_LINE" || true
        ;;
    esac

    # Always try .profile as universal fallback if nothing updated yet
    if [ "$added" -eq 0 ]; then
      add_path_to_file "$HOME/.profile" "$BASH_LINE" || true
    fi
    # And ensure at least bashrc/zshrc gets a line if both missing but home exists
    if [ "$added" -eq 0 ]; then
      # Last resort: create .profile
      if [ ! -f "$HOME/.profile" ] && [ -d "$HOME" ]; then
        {
          echo "# Added by Forcefield installer"
          echo "$BASH_LINE"
        } > "$HOME/.profile" 2>/dev/null && {
          ui_detail "Created $HOME/.profile"
          added=1
        } || true
      fi
    fi

    if [ "$added" -eq 0 ]; then
      ui_warn "could not automatically update shell config (no writable rc file found)"
    else
      ui_done "PATH configured"
    fi
  fi
fi

# ---------- final output ----------
printf "\n"
ui_rule
if [ -n "${INSTALLED_VER:-}" ]; then
  case "$INSTALLED_VER" in
    "("* ) ui_done "Forcefield $VERSION installed" ;;
    *) ui_done "Forcefield installed ($INSTALLED_VER)" ;;
  esac
else
  ui_done "Forcefield $VERSION installed"
fi
ui_row "Location" "$EXISTING_BIN"

if [ "$NEEDS_PATH_UPDATE" -eq 1 ]; then
  ui_warn "$INSTALL_DIR is not in your current PATH."
  if [ "$NO_MODIFY_PATH" != "1" ]; then
    ui_detail "The installer tried to add it to your shell config."
    ui_detail "Restart your terminal or run:"
    ui_detail "export PATH=\"$INSTALL_DIR:\$PATH\""
  else
    ui_detail "Add it manually:"
    ui_detail "export PATH=\"$INSTALL_DIR:\$PATH\""
    ui_detail "And add that line to ~/.bashrc or ~/.zshrc to persist."
  fi
else
  ui_row "PATH" "configured"
fi
printf '%s Next: run %s to check your setup\n' "$G_PEND" "ff doctor"

if [ "$CHECKSUM_OK" -eq 0 ]; then
  ui_detail "Note: checksum verification was skipped or unavailable. For production, use a release with checksums.txt."
fi

# cleanup handled by trap
} # end main()

# ---------- entrypoint ----------
if [ "${FORCEFIELD_LIB_ONLY:-0}" = "1" ]; then
  : # library mode for tests: definitions only, no execution
else
  main "$@"
fi
