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

# ---------- helpers ----------

info() { printf "[INFO] %s\n" "$*"; }
warn() { printf "[WARN] %s\n" "$*" >&2; }
err()  { printf "[ERROR] %s\n" "$*" >&2; }

die() {
  err "$*"
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
  -h, --help        Show this help

Environment:
  FORCEFIELD_VERSION      Same as --version
  FORCEFIELD_INSTALL_DIR  Same as --dir
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
  # $1 = url, $2 = dest
  url="$1"
  dest="$2"
  if need_cmd curl; then
    curl -fsSL -o "$dest" "$url"
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
    warn "no sha256 tool found (sha256sum/shasum/openssl); skipping checksum verification"
    return 1
  fi
}

# ---------- argument parsing ----------

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
    warn "install dir is not absolute: $INSTALL_DIR (resolving relative to current directory)"
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

# ---------- detect platform ----------
OS="$(detect_os)"
ARCH="$(detect_arch)"
info "Detected: ${OS}/${ARCH}"

# ---------- resolve version ----------
if [ -z "$VERSION" ]; then
  info "Resolving latest release from GitHub..."
  VERSION="$(fetch_latest_version)"
  info "Latest release: $VERSION"
else
  # normalize: ensure v prefix
  case "$VERSION" in
    v*) ;;
    *) VERSION="v$VERSION" ;;
  esac
  info "Requested version: $VERSION"
fi

validate_version "$VERSION"

ARTIFACT="ff-${OS}-${ARCH}"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARTIFACT}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"
# Fallback alt name for old releases (checksums.txt vs SHA256SUMS) - we handle via try

info "Artifact: $ARTIFACT"
info "URL: $DOWNLOAD_URL"

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
info "Downloading $DOWNLOAD_URL ..."
if ! download_file "$DOWNLOAD_URL" "$TMP/$ARTIFACT"; then
  err "download failed: $DOWNLOAD_URL"
  err "The artifact may not exist for ${OS}/${ARCH} at $VERSION."
  err "Check https://github.com/${REPO}/releases/tag/${VERSION} and supported architectures: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64"
  exit 1
fi

if [ ! -s "$TMP/$ARTIFACT" ]; then
  die "downloaded file is empty: $TMP/$ARTIFACT (check $DOWNLOAD_URL)"
fi

# ---------- checksum ----------
info "Verifying checksum..."
CHECKSUM_FILE="$TMP/checksums.txt"
CHECKSUM_OK=0
CHECKSUM_TMP="$TMP/checksums.tmp"

# Try primary checksums.txt, then fallback to SHA256SUMS / checksums.txt variations
for url in "$CHECKSUM_URL" "https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS" "https://github.com/${REPO}/releases/download/${VERSION}/checksums.sha256"; do
  if download_file "$url" "$CHECKSUM_TMP" 2>/dev/null; then
    if [ -s "$CHECKSUM_TMP" ]; then
      mv "$CHECKSUM_TMP" "$CHECKSUM_FILE"
      info "Found checksums at $url"
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
    warn "checksums.txt does not contain $ARTIFACT; skipping verification (this should not happen for new releases)"
  else
    # Normalize to lowercase
    expected="$(printf "%s" "$expected" | tr 'A-F' 'a-f')"
    actual="$(sha256_of_file "$TMP/$ARTIFACT" 2>/dev/null || true)"
    if [ -z "$actual" ]; then
      warn "could not compute sha256; skipping verification"
    else
      actual="$(printf "%s" "$actual" | tr 'A-F' 'a-f')"
      if [ "$expected" != "$actual" ]; then
        err "checksum mismatch for $ARTIFACT"
        err "  expected: $expected"
        err "  actual:   $actual"
        err "Refusing to install. The download may be corrupted or tampered with."
        exit 1
      fi
      info "Checksum OK: $actual"
      CHECKSUM_OK=1
    fi
  fi
else
  warn "no checksums file at $CHECKSUM_URL"
  warn "skipping verification (old releases before v1.2 may not have checksums)"
  warn "for security, prefer a release with checksums.txt or verify manually"
fi

# ---------- install ----------
info "Installing to $INSTALL_DIR ..."
mkdir -p "$INSTALL_DIR" || die "failed to create $INSTALL_DIR"

# Detect existing installation
EXISTING_BIN="$INSTALL_DIR/$BINARY"
if [ -f "$EXISTING_BIN" ]; then
  info "Existing installation found at $EXISTING_BIN (upgrading in place)"
else
  info "No existing installation at $EXISTING_BIN"
fi

# Never touch ~/.forcefield - explicitly ensure we are not writing there unless user chose it
# If install dir is inside ~/.forcefield, warn but still allow if explicitly requested?
case "$INSTALL_DIR" in
  "$HOME/.forcefield" | "$HOME/.forcefield"/*)
    warn "install dir is inside ~/.forcefield; sessions/config live there. This is allowed but unusual."
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

info "Installed $EXISTING_BIN"

# ---------- verify ----------
if [ -x "$EXISTING_BIN" ]; then
  if "$EXISTING_BIN" --version >/dev/null 2>&1; then
    INSTALLED_VER="$("$EXISTING_BIN" --version 2>/dev/null | head -n 1 || true)"
    info "Verified: $INSTALLED_VER"
  else
    # Try without --version (some older dev builds)
    warn "installed binary does not support --version; checking help instead"
    if ! "$EXISTING_BIN" --help >/dev/null 2>&1; then
      warn "installed binary failed to run --help; it may be the wrong architecture"
    fi
    INSTALLED_VER="(unknown, use ff --version after fixing PATH)"
  fi
else
  warn "installed binary is not executable: $EXISTING_BIN"
fi

# ---------- PATH ----------
NEEDS_PATH_UPDATE=0
if is_in_path "$INSTALL_DIR"; then
  info "PATH already contains $INSTALL_DIR"
else
  NEEDS_PATH_UPDATE=1
  if [ "$NO_MODIFY_PATH" = "1" ]; then
    warn "PATH does not contain $INSTALL_DIR (path modification disabled via --no-modify-path)"
  else
    info "PATH does not contain $INSTALL_DIR; attempting to add it to shell config..."

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
        info "  $file already references $INSTALL_DIR (skipping)"
        added=1
        return 0
      fi
      # Append with a marker comment
      {
        echo ""
        echo "# Added by Forcefield installer ($(date -u +%Y-%m-%d 2>/dev/null || echo install))"
        echo "$line"
      } >> "$file" 2>/dev/null || return 1
      info "  Updated $file"
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
          info "  Created $HOME/.profile"
          added=1
        } || true
      fi
    fi

    if [ "$added" -eq 0 ]; then
      warn "could not automatically update shell config (no writable rc file found)"
    fi
  fi
fi

# ---------- final output ----------
printf "\n"
printf "Forcefield installed successfully!\n"
printf "  Binary:  %s\n" "$EXISTING_BIN"
if [ -n "${INSTALLED_VER:-}" ]; then
  printf "  Version: %s\n" "$INSTALLED_VER"
else
  printf "  Version: %s (run ff --version)\n" "$VERSION"
fi
printf "  Install dir: %s\n" "$INSTALL_DIR"

if [ "$NEEDS_PATH_UPDATE" -eq 1 ]; then
  printf "\n"
  printf "PATH update needed:\n"
  printf "  %s is not in your current PATH.\n" "$INSTALL_DIR"
  if [ "$NO_MODIFY_PATH" != "1" ]; then
    printf "  The installer tried to add it to your shell config.\n"
    printf "  Restart your terminal or run:\n"
    printf "    export PATH=\"%s:\$PATH\"\n" "$INSTALL_DIR"
  else
    printf "  Add it manually:\n"
    printf "    export PATH=\"%s:\$PATH\"\n" "$INSTALL_DIR"
    printf "  And add that line to ~/.bashrc or ~/.zshrc to persist.\n"
  fi
  printf "\n"
  printf "After updating PATH, verify with:\n"
  printf "  ff --version\n"
  printf "  ff doctor\n"
else
  printf "  Run: ff --version\n"
  printf "       ff doctor\n"
fi

if [ "$CHECKSUM_OK" -eq 0 ]; then
  printf "\nNote: checksum verification was skipped or unavailable. For production, use a release with checksums.txt.\n"
fi

printf "\n"
printf "Documentation: https://github.com/%s\n" "$REPO"
printf "Releases:      https://github.com/%s/releases\n" "$REPO"
printf "\n"

# cleanup handled by trap
