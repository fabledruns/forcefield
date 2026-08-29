#!/bin/sh
# Forcefield uninstaller for Linux and macOS
# Removes only the installed binary. Never touches ~/.forcefield
set -eu

BINARY="ff"
INSTALL_DIR="${FORCEFIELD_INSTALL_DIR:-}"

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

while [ $# -gt 0 ]; do
  case "$1" in
    --dir|--install-dir)
      if [ $# -lt 2 ]; then echo "[ERROR] --dir requires an argument" >&2; exit 1; fi
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
      echo "[ERROR] unknown option: $1" >&2; exit 1
      ;;
    *)
      echo "[ERROR] unexpected argument: $1" >&2; exit 1
      ;;
  esac
done

if [ -z "${HOME:-}" ]; then
  echo "[ERROR] HOME is not set; pass --dir <path>" >&2; exit 1
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
    echo "[ERROR] Refusing to uninstall from inside ~/.forcefield ($INSTALL_DIR)" >&2
    echo "This directory holds your config/sessions. Pass a different --dir." >&2
    exit 1
    ;;
esac

TARGET="$INSTALL_DIR/$BINARY"
TARGET_EXE="$INSTALL_DIR/${BINARY}.exe"

found=0
for candidate in "$TARGET" "$TARGET_EXE"; do
  if [ -f "$candidate" ]; then
    echo "[INFO] Removing $candidate"
    rm -f "$candidate" || {
      echo "[ERROR] Failed to remove $candidate (permission denied?)" >&2
      exit 1
    }
    found=1
  fi
done

if [ "$found" -eq 0 ]; then
  echo "[INFO] No Forcefield binary found at $TARGET (already removed?)"
else
  echo "[INFO] Removed Forcefield binary from $INSTALL_DIR"
fi

# Check if still on PATH (stale)
if command -v "$BINARY" >/dev/null 2>&1; then
  where="$(command -v "$BINARY" 2>/dev/null || true)"
  echo "[WARN] ff is still found on PATH at: $where"
  echo "       You may have another installation elsewhere."
else
  echo "[INFO] ff is no longer on PATH"
fi

cat <<EOF

Uninstall complete.

What was removed:
  $INSTALL_DIR/$BINARY  (and $BINARY.exe if present)

What remains (never removed by uninstaller):
  ~/.forcefield/config.yaml
  ~/.forcefield/skills/
  ~/.forcefield/.env
  .forcefield/sessions/   (per-project)
  project memory / .env

To remove those manually, delete ~/.forcefield if you want a full reset
(note: this deletes your config and skills, but not project sessions).

Your shell config (PATH entry for $INSTALL_DIR) was NOT modified.
If you want to remove it, delete the line added by the installer from:
  ~/.bashrc  ~/.zshrc  ~/.profile  ~/.config/fish/config.fish
EOF
