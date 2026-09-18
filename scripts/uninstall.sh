#!/bin/sh
# Forcefield uninstaller for Linux and macOS
# Removes only the installed binary. Never touches ~/.forcefield
set -eu

BINARY="ff"
INSTALL_DIR="${FORCEFIELD_INSTALL_DIR:-}"

# ---------- terminal UI (presentation only) ----------
# Mirrored from install.sh (single-file script: must stay self-contained).
# Keep in sync: detect_ui_mode, palette, glyphs, brand, steps, die.
# Palette mirrors internal/tui/styles.go; glyphs mirror
# internal/tui/icons.go.

UI_COLOR="plain"
UI_GLYPHS="ascii"
C_ACCENT=""; C_TEXT=""; C_MUTED=""; C_ERROR=""; C_SUCCESS=""; C_WARN=""; C_RESET=""
C_BOLD=""; C_BOLD_ACCENT=""
G_OK="*"; G_STEP=">"; G_PEND="o"; G_FAIL="x"; G_WARN="!"; G_ARROW="->"
UI_RULE_LEN=40

ui_init() {
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
  if [ "$UI_GLYPHS" = "unicode" ]; then
    G_OK="✓"; G_STEP="›"; G_PEND="○"; G_FAIL="✕"; G_WARN="!"; G_ARROW="→"
  else
    G_OK="*"; G_STEP=">"; G_PEND="o"; G_FAIL="x"; G_WARN="!"; G_ARROW="->"
  fi
}

brand() {
  if [ "$UI_COLOR" = "color" ]; then
    printf '%sFORCE%s%sFIELD%s\n' "$C_BOLD_ACCENT" "$C_RESET" "$C_BOLD" "$C_RESET"
    printf '%sthe local-first agent harness%s\n' "$C_MUTED" "$C_RESET"
  else
    printf 'FORCEFIELD\nthe local-first agent harness\n'
  fi
}

ui_rule() {
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
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s%s%s %s\n' "$C_ACCENT" "$G_STEP" "$C_RESET" "$*"
  else
    printf '%s %s\n' "$G_STEP" "$*"
  fi
}

ui_done() {
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s%s%s %s\n' "$C_SUCCESS" "$G_OK" "$C_RESET" "$*"
  else
    printf '%s %s\n' "$G_OK" "$*"
  fi
}

ui_warn() {
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s%s%s %s\n' "$C_WARN" "$G_WARN" "$C_RESET" "$*" >&2
  else
    printf '%s %s\n' "$G_WARN" "$*" >&2
  fi
}

ui_row() {
  printf '  %-12s %s\n' "$1" "$2"
}

ui_detail() {
  if [ "$UI_COLOR" = "color" ]; then
    printf '%s  %s%s\n' "$C_MUTED" "$*" "$C_RESET"
  else
    printf '  %s\n' "$*"
  fi
}

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

die() {
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

usage() {
  cat <<'EOF'
Forcefield uninstaller

Usage:
  uninstall.sh [options]

Options:
  --dir <path>  Install directory to remove from. Default: $HOME/.local/bin
  -h, --help    Show this help

Removes only the Forcefield binary. The following are NEVER removed:
  ~/.forcefield/config.yaml
  ~/.forcefield/skills/
  ~/.forcefield/.env
  project .forcefield/sessions/ or memory
EOF
}

# ---------- main ----------
# The uninstall flow lives in main() so the file can also be sourced
# with FORCEFIELD_LIB_ONLY=1 for testing: sourcing loads the helpers
# without executing anything.
main() {
while [ $# -gt 0 ]; do
  case "$1" in
    --dir|--install-dir)
      if [ $# -lt 2 ]; then die "--dir requires an argument"; fi
      INSTALL_DIR="$2"
      shift 2
      ;;
    --dir=*)
      INSTALL_DIR="${1#--dir=}"
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

ui_init

if [ -z "${HOME:-}" ]; then
  die "HOME is not set; pass --dir <path>"
fi

if [ -z "$INSTALL_DIR" ]; then
  INSTALL_DIR="$HOME/.local/bin"
fi

case "$INSTALL_DIR" in
  "~") INSTALL_DIR="$HOME" ;;
  "~/"*) INSTALL_DIR="$HOME/${INSTALL_DIR#\~/}" ;;
esac

# Safety: never delete ~/.forcefield
case "$INSTALL_DIR" in
  "$HOME/.forcefield" | "$HOME/.forcefield"/*)
    die "Refusing to uninstall from inside ~/.forcefield ($INSTALL_DIR)
This directory holds your config/sessions. Pass a different --dir."
    ;;
esac

TARGET="$INSTALL_DIR/$BINARY"
TARGET_EXE="$INSTALL_DIR/${BINARY}.exe"

brand
ui_rule
printf "\n"

ui_step "Detecting installation"
ui_row "Location" "$TARGET"
TARGET_VER="$(installed_version_of "$TARGET" || true)"
if [ -z "$TARGET_VER" ]; then
  TARGET_VER="$(installed_version_of "$TARGET_EXE" || true)"
fi
if [ -n "$TARGET_VER" ]; then
  ui_row "Installed" "$TARGET_VER"
elif [ -f "$TARGET" ] || [ -f "$TARGET_EXE" ]; then
  ui_row "Installed" "unknown"
else
  ui_row "Installed" "none"
fi

found=0
if [ -f "$TARGET" ] || [ -f "$TARGET_EXE" ]; then
  ui_step "Removing binary"
fi
for candidate in "$TARGET" "$TARGET_EXE"; do
  if [ -f "$candidate" ]; then
    ui_detail "Removing $candidate"
    rm -f "$candidate" || die "Failed to remove $candidate (permission denied?)"
    found=1
  fi
done

if [ "$found" -eq 0 ]; then
  ui_done "Forcefield is not installed"
else
  ui_done "Binary removed"
  ui_row "Removed" "$TARGET"
fi

# Check if still on PATH (stale)
if command -v "$BINARY" >/dev/null 2>&1; then
  where="$(command -v "$BINARY" 2>/dev/null || true)"
  ui_warn "ff is still found on PATH at: $where"
  ui_detail "You may have another installation elsewhere."
else
  ui_done "ff is no longer on PATH"
fi

ui_detail "Kept (never removed): ~/.forcefield config, skills, .env; per-project sessions and memory."
ui_detail "To fully reset, delete ~/.forcefield manually (config and skills go too; per-project sessions stay)."
ui_detail "Shell config was not modified."
ui_detail "To drop the PATH entry, delete the installer line from ~/.bashrc, ~/.zshrc, ~/.profile, or ~/.config/fish/config.fish."
if [ "$found" -eq 1 ]; then
  ui_done "Forcefield uninstalled"
fi
} # end main()

# ---------- entrypoint ----------
if [ "${FORCEFIELD_LIB_ONLY:-0}" = "1" ]; then
  : # library mode for tests: definitions only, no execution
else
  main "$@"
fi
