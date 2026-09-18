#!/bin/sh
# Behavior tests for scripts/install.sh.
#
# No network, no side effects: the script is sourced with
# FORCEFIELD_LIB_ONLY=1, and every behavior under test runs against
# temporary directories and stub binaries.
#
# Run from the repository root:
#   sh scripts/tests/test-install-sh.sh
#
# POSIX sh only (dash-clean). Hand-rolled asserts, no framework.
set -eu

HERE="$(dirname "$0")"
INSTALL_SH="$HERE/../install.sh"
UNINSTALL_SH="$HERE/../uninstall.sh"
TEST_SH="${FORCEFIELD_TEST_SH:-sh}"

pass=0
fail=0

# ESC for ANSI-leak assertions (plain output must never contain it).
ESC="$(printf '\033')"

assert_eq() {
  # assert_eq <description> <expected> <actual>
  desc="$1"
  expected="$2"
  actual="$3"
  if [ "$expected" = "$actual" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf "FAIL - %s\n  expected: <%s>\n  actual:   <%s>\n" "$desc" "$expected" "$actual"
  fi
}

assert_true() {
  # assert_true <description> <exit-status>
  desc="$1"
  status="$2"
  if [ "$status" -eq 0 ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf "FAIL - %s (exit status %s, want 0)\n" "$desc" "$status"
  fi
}

check_exit() {
  # check_exit <description> <want-status> <command> [args...]
  # Runs the command in a subshell (die() exits there, not here).
  desc="$1"
  want="$2"
  shift 2
  set +e
  ( "$@" >/dev/null 2>&1 )
  got=$?
  set -e
  assert_eq "$desc" "$want" "$got"
}

# ---------- regression: sourcing must not execute ----------

lib_bytes="$(FORCEFIELD_LIB_ONLY=1 "$TEST_SH" "$INSTALL_SH" 2>&1 | wc -c | tr -d ' ')"
assert_eq "lib-only source produces no output" "0" "$lib_bytes"

FORCEFIELD_LIB_ONLY=1 "$TEST_SH" "$INSTALL_SH" >/dev/null 2>&1
assert_true "lib-only source exits 0" "$?"

"$TEST_SH" -c 'FORCEFIELD_LIB_ONLY=1 . "$1"; type validate_version >/dev/null 2>&1 && type detect_arch >/dev/null 2>&1 && type detect_os >/dev/null 2>&1 && type is_in_path >/dev/null 2>&1' _ "$INSTALL_SH"
assert_true "lib-only source exposes helpers" "$?"

# ---------- regression: uninstall sourcing must not execute ----------

lib_bytes="$(FORCEFIELD_LIB_ONLY=1 "$TEST_SH" "$UNINSTALL_SH" 2>&1 | wc -c | tr -d ' ')"
assert_eq "lib-only uninstall source produces no output" "0" "$lib_bytes"

FORCEFIELD_LIB_ONLY=1 "$TEST_SH" "$UNINSTALL_SH" >/dev/null 2>&1
assert_true "lib-only uninstall source exits 0" "$?"

"$TEST_SH" -c 'FORCEFIELD_LIB_ONLY=1 . "$1"; type usage >/dev/null 2>&1' _ "$UNINSTALL_SH"
assert_true "lib-only uninstall source exposes helpers" "$?"

# Load helpers for the behavior tests below (no execution: LIB_ONLY).
FORCEFIELD_LIB_ONLY=1
# shellcheck disable=SC1090
. "$INSTALL_SH"
unset FORCEFIELD_LIB_ONLY

# ---------- version behavior ----------

assert_eq "normalize strips v prefix" "1.2.3" "$(normalize_version "v1.2.3")"
assert_eq "normalize keeps bare version" "1.2.3" "$(normalize_version "1.2.3")"
assert_eq "normalize strips build metadata" "1.2.0-rc.1" "$(normalize_version "v1.2.0-rc.1+001")"
assert_eq "normalize empty stays empty" "" "$(normalize_version "")"

assert_eq "compare equal versions" "0" "$(compare_versions "v1.2.3" "1.2.3")"
assert_eq "compare older installed" "-1" "$(compare_versions "v1.1.0" "v1.2.0")"
assert_eq "compare newer installed" "1" "$(compare_versions "v2.0.0" "v1.9.9")"
assert_eq "compare patch versions" "-1" "$(compare_versions "1.2.3" "1.2.10")"
assert_eq "compare prerelease below release" "-1" "$(compare_versions "v1.2.3-rc.1" "v1.2.3")"
assert_eq "compare missing parts as zero" "0" "$(compare_versions "v1.2" "1.2.0")"
assert_eq "compare uppercase V prefix" "0" "$(compare_versions "V1.2.3" "v1.2.3")"
assert_eq "compare build metadata ignored" "0" "$(compare_versions "v1.2.0+001" "1.2.0+002")"
assert_eq "compare rc ordering" "-1" "$(compare_versions "v1.2.3-rc.1" "v1.2.3-rc.2")"
assert_eq "compare major beats minor" "1" "$(compare_versions "v2.0.0" "v1.99.99")"

# validate_version dies (nonzero) on malformed input, passes (zero) on
# valid input. die() exits, so check_exit runs each case in a subshell.
check_exit "validate accepts v1.2.3" "0" validate_version "v1.2.3"
check_exit "validate accepts prerelease" "0" validate_version "1.0.0-rc.1"
check_exit "validate accepts empty (means latest)" "0" validate_version ""
check_exit "validate rejects metachars" "1" validate_version "v1.0; rm -rf /"
check_exit "validate rejects traversal" "1" validate_version "../evil"
# ---------- installation path behavior ----------

assert_eq "default dir is under home" "$HOME/.local/bin" "$(default_install_dir)"
assert_eq "tilde expands to home" "$HOME/bin" "$(expand_tilde "~/bin")"
assert_eq "bare tilde expands to home" "$HOME" "$(expand_tilde "~")"
assert_eq "plain path untouched" "/opt/ff" "$(expand_tilde "/opt/ff")"
assert_eq "normalize strips trailing slash" "/opt/ff" "$(normalize_dir "/opt/ff/")"
assert_eq "normalize keeps root" "/" "$(normalize_dir "/")"
assert_eq "normalize keeps clean path" "/opt/ff" "$(normalize_dir "/opt/ff")"

OLD_PATH="$PATH"
check_path() {
  # check_path <description> <want-status> <dir>: set -e safe membership check
  desc="$1"
  want="$2"
  dir="$3"
  if is_in_path "$dir"; then
    got="0"
  else
    got="1"
  fi
  assert_eq "$desc" "$want" "$got"
}
PATH="/usr/bin:/opt/ff/bin"
check_path "is_in_path finds exact entry" "0" "/opt/ff/bin"
check_path "is_in_path rejects substring" "1" "/opt/ff"
PATH="/usr/bin"
check_path "is_in_path reports missing" "1" "/opt/ff/bin"
PATH=""
check_path "is_in_path handles empty PATH" "1" "/opt/ff/bin"
PATH="$OLD_PATH"

check_exit "default dir fails without HOME" "1" sh -c 'unset HOME; FORCEFIELD_LIB_ONLY=1 . "$0"; default_install_dir' "$INSTALL_SH"

# ---------- UI mode detection ----------
# detect_ui_mode prints "<color|plain>+<unicode|ascii>".
# The test harness pipes stdout (not a TTY), so the default is plain.
# Each case runs in a subshell so temporary env never leaks.

assert_eq "piped output is plain" "plain+unicode" "$(
  LC_ALL=C.UTF-8; TERM=xterm; FORCEFIELD_TTY=0; detect_ui_mode
)"
assert_eq "NO_COLOR forces plain" "plain+unicode" "$(
  LC_ALL=C.UTF-8; TERM=xterm; FORCEFIELD_TTY=1; NO_COLOR=1; detect_ui_mode
)"
assert_eq "dumb terminal is plain" "plain+unicode" "$(
  LC_ALL=C.UTF-8; TERM=dumb; FORCEFIELD_TTY=1; detect_ui_mode
)"
assert_eq "tty plus utf-8 is full color" "color+unicode" "$(
  LC_ALL=C.UTF-8; TERM=xterm; FORCEFIELD_TTY=1; detect_ui_mode
)"
assert_eq "ascii override wins" "color+ascii" "$(
  LC_ALL=C.UTF-8; TERM=xterm; FORCEFIELD_TTY=1; FORCEFIELD_ASCII=1; detect_ui_mode
)"
assert_eq "non-utf8 locale is ascii" "color+ascii" "$(
  LC_ALL=C; TERM=xterm; FORCEFIELD_TTY=1; detect_ui_mode
)"
assert_eq "missing TERM with COLORTERM still colors" "color+unicode" "$(
  LC_ALL=C.UTF-8; unset TERM; COLORTERM=truecolor; FORCEFIELD_TTY=1; detect_ui_mode
)"
assert_eq "missing TERM and COLORTERM stays plain" "plain+unicode" "$(
  LC_ALL=C.UTF-8; unset TERM; unset COLORTERM; FORCEFIELD_TTY=1; detect_ui_mode
)"

# ---------- argument parsing ----------

VERSION=""; INSTALL_DIR=""; NO_MODIFY_PATH="0"; FORCE="0"
parse_args --force --version v1.0.0 --dir /tmp/ff-test
assert_eq "parse sets version" "v1.0.0" "$VERSION"
assert_eq "parse sets force" "1" "$FORCE"
assert_eq "parse sets dir" "/tmp/ff-test" "$INSTALL_DIR"

VERSION=""; FORCE="0"
parse_args --version=1.2.3 --no-modify-path
assert_eq "parse equals-form version" "1.2.3" "$VERSION"
assert_eq "parse no-modify-path" "1" "$NO_MODIFY_PATH"

check_exit "parse rejects unknown flag" "1" parse_args --bogus
check_exit "parse rejects empty version" "1" parse_args --version ""
assert_eq "env enables force" "1" "$(FORCEFIELD_FORCE=1 "$TEST_SH" -c 'FORCEFIELD_LIB_ONLY=1 . "$0"; printf "%s" "$FORCE"' "$INSTALL_SH")"
assert_eq "force defaults off" "0" "$("$TEST_SH" -c 'FORCEFIELD_LIB_ONLY=1 . "$0"; printf "%s" "$FORCE"' "$INSTALL_SH")"

ACTION_DIR="$(mktemp -d "${TMPDIR:-/tmp}/forcefield-action.XXXXXX")"
trap 'rm -rf "${STUBS:-}" "${ACTION_DIR:-}" 2>/dev/null || true' EXIT INT TERM HUP

make_installed() {
  # make_installed <version-line>: places a fake ff in ACTION_DIR/bin
  mkdir -p "$ACTION_DIR/bin"
  {
    printf "#!/bin/sh\n"
    printf "printf '%%s\\n' \"%s\"\n" "$1"
    printf "exit 0\n"
  } > "$ACTION_DIR/bin/ff"
  chmod +x "$ACTION_DIR/bin/ff"
}

assert_eq "action fresh when dir empty" "fresh" "$(
  rm -rf "$ACTION_DIR/bin"; resolve_install_action "v1.2.0" "0" "$ACTION_DIR/bin"
)"
make_installed "ff version v1.1.0"
assert_eq "action upgrades older install" "upgrade" "$(resolve_install_action "v1.2.0" "0" "$ACTION_DIR/bin")"
make_installed "ff version v1.2.0"
assert_eq "action current when equal" "current" "$(resolve_install_action "v1.2.0" "0" "$ACTION_DIR/bin")"
assert_eq "action reinstalls equal with force" "reinstall" "$(resolve_install_action "v1.2.0" "1" "$ACTION_DIR/bin")"
make_installed "ff version v2.0.0"
assert_eq "action refuses downgrade" "refuse-downgrade" "$(resolve_install_action "v1.2.0" "0" "$ACTION_DIR/bin")"
assert_eq "action force overrides downgrade" "reinstall" "$(resolve_install_action "v1.2.0" "1" "$ACTION_DIR/bin")"

# ---------- gate end-to-end through main() (Linux only) ----------
# Pinned versions need no network: the gate decides before any download.
# main() exits, so each case runs in a subshell.
if [ "$(uname -s 2>/dev/null || echo unknown)" = "Linux" ]; then
  GATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/forcefield-gate.XXXXXX")"
  mkdir -p "$GATE_DIR/bin"
  {
    printf "#!/bin/sh\n"
    printf "printf '%%s\\n' \"ff version v1.2.0\"\n"
    printf "exit 0\n"
  } > "$GATE_DIR/bin/ff"
  chmod +x "$GATE_DIR/bin/ff"

  set +e
  gate_out="$( (main --dir "$GATE_DIR/bin" --version v1.2.0) 2>&1 )"
  gate_code=$?
  set -e
  assert_eq "gate exits 0 when current" "0" "$gate_code"
  case "$gate_out" in
    *"already installed"*) pass=$((pass + 1)) ;;
    *) fail=$((fail + 1)); printf "FAIL - gate reports already-installed:\n%s\n" "$gate_out" ;;
  esac
  case "$gate_out" in
    *"$ESC"*) fail=$((fail + 1)); printf "FAIL - piped gate output leaks ANSI:\n%s\n" "$gate_out" ;;
    *) pass=$((pass + 1)) ;;
  esac

  set +e
  gate_out="$( (main --dir "$GATE_DIR/bin" --version v1.0.0) 2>&1 )"
  gate_code=$?
  set -e
  assert_eq "gate exits 1 on downgrade" "1" "$gate_code"
  case "$gate_out" in
    *"refusing to downgrade"*) pass=$((pass + 1)) ;;
    *) fail=$((fail + 1)); printf "FAIL - gate refuses downgrade:\n%s\n" "$gate_out" ;;
  esac
  rm -rf "$GATE_DIR" 2>/dev/null || true
fi

# ---------- installed version parsing (stub binaries, no network) ----------

STUBS="$(mktemp -d "${TMPDIR:-/tmp}/forcefield-stubs.XXXXXX")"
cleanup_stubs() { rm -rf "$STUBS" 2>/dev/null || true; }
trap cleanup_stubs EXIT INT TERM HUP

make_stub() {
  # make_stub <name> <exit-code> <output...>: writes an executable stub
  name="$1"
  code="$2"
  shift 2
  {
    printf "#!/bin/sh\n"
    printf "printf '%%s\\n' \"%s\"\n" "$*"
    printf "exit %s\n" "$code"
  } > "$STUBS/$name"
  chmod +x "$STUBS/$name"
}

make_stub "ff-good" 0 "ff version v1.2.3"
assert_eq "installed parses ff --version" "v1.2.3" "$(installed_version_of "$STUBS/ff-good")"

make_stub "ff-old" 1 "ff version v1.0.0"
assert_eq "installed parses despite nonzero exit" "v1.0.0" "$(installed_version_of "$STUBS/ff-old")"

make_stub "ff-dev" 0 "ff version dev"
assert_eq "installed dev passes through" "dev" "$(installed_version_of "$STUBS/ff-dev")"

make_stub "ff-garbage" 0 "hello world"
assert_eq "installed garbage yields empty" "" "$(installed_version_of "$STUBS/ff-garbage" || true)"

assert_eq "installed missing binary yields empty" "" "$(installed_version_of "$STUBS/nope" || true)"

# ---------- upgrade decision ----------

assert_eq "decision fresh when absent" "fresh" "$(decide_install_action "" "v1.2.0" "0" "0")"
assert_eq "decision upgrade when older" "upgrade" "$(decide_install_action "v1.1.0" "v1.2.0" "0" "1")"
assert_eq "decision current when equal" "current" "$(decide_install_action "v1.2.0" "v1.2.0" "0" "1")"
assert_eq "decision reinstall when equal plus force" "reinstall" "$(decide_install_action "v1.2.0" "v1.2.0" "1" "1")"
assert_eq "decision downgrade refused" "refuse-downgrade" "$(decide_install_action "v2.0.0" "v1.2.0" "0" "1")"
assert_eq "decision downgrade with force reinstalls" "reinstall" "$(decide_install_action "v2.0.0" "v1.2.0" "1" "1")"
assert_eq "decision unknown version upgrades" "upgrade" "$(decide_install_action "dev" "v1.2.0" "0" "1")"
assert_eq "decision empty version upgrades" "upgrade" "$(decide_install_action "" "v1.2.0" "0" "1")"

# ---------- UI rendering invariants ----------
# Rendering reads UI_COLOR/UI_GLYPHS set by ui_init. Tests pin the mode
# explicitly (no terminal dependence) and assert small invariants, never
# full-screen snapshots.

ui_init "plain+ascii"
assert_eq "ui_init parses plain mode" "plain" "$UI_COLOR"
assert_eq "ui_init parses ascii glyphs" "ascii" "$UI_GLYPHS"
ui_init "color+unicode"
assert_eq "ui_init parses color mode" "color" "$UI_COLOR"
assert_eq "ui_init parses unicode glyphs" "unicode" "$UI_GLYPHS"

ui_init "plain+unicode"
assert_eq "unicode done glyph" "✓" "$G_OK"
assert_eq "unicode active glyph" "›" "$G_STEP"
assert_eq "unicode pending glyph" "○" "$G_PEND"
assert_eq "unicode failure glyph" "✕" "$G_FAIL"
assert_eq "unicode warning glyph" "!" "$G_WARN"
ui_init "plain+ascii"
assert_eq "ascii done glyph" "*" "$G_OK"
assert_eq "ascii active glyph" ">" "$G_STEP"
assert_eq "ascii pending glyph" "o" "$G_PEND"
assert_eq "ascii failure glyph" "x" "$G_FAIL"
assert_eq "ascii warning glyph" "!" "$G_WARN"

ui_init "plain+ascii"
plain_out="$(brand; ui_step "Doing"; ui_done "Did"; ui_row "Label" "value"; ui_warn "careful" 2>&1)"
case "$plain_out" in
  *"$ESC"*) fail=$((fail + 1)); printf "FAIL - plain mode emits no ANSI escapes\n" ;;
  *) pass=$((pass + 1)) ;;
esac

ui_init "plain+unicode"
brand_out="$(brand)"
case "$brand_out" in
  *"FORCE"*|*"FIELD"*) pass=$((pass + 1)) ;;
  *) fail=$((fail + 1)); printf "FAIL - brand names Forcefield\n%s\n" "$brand_out" ;;
esac
case "$brand_out" in
  *"local-first agent harness"*) pass=$((pass + 1)) ;;
  *) fail=$((fail + 1)); printf "FAIL - brand carries the tagline\n%s\n" "$brand_out" ;;
esac

assert_eq "row aligns label and value" "  AB           c" "$(ui_row "AB" "c")"

set +e
die_out="$(ui_init "plain+unicode" >/dev/null; die "boom" 2>&1)"
die_code=$?
set -e
assert_eq "die exits 1" "1" "$die_code"
case "$die_out" in
  *"✕"*) pass=$((pass + 1)) ;;
  *) fail=$((fail + 1)); printf "FAIL - die marks failure with glyph:\n%s\n" "$die_out" ;;
esac

ui_init "color+unicode" >/dev/null 2>&1
color_probe="$(COLORTERM=truecolor TERM=xterm-256color ui_setup_palette >/dev/null 2>&1; printf "%s" "$C_ACCENT")"
case "$color_probe" in
  "") fail=$((fail + 1)); printf "FAIL - color mode defines the accent\n" ;;
  *) pass=$((pass + 1)) ;;
esac

# ---------- uninstall behavior (jailed HOME, no network) ----------
# uninstall.sh defines its own main()/usage(), so each case runs in a
# child sh that sources uninstall.sh with LIB_ONLY. Callers wrap capture
# with set +e because main() exits.

UJAIL="$(mktemp -d "${TMPDIR:-/tmp}/forcefield-ujail.XXXXXX")"
trap 'rm -rf "${STUBS:-}" "${ACTION_DIR:-}" "${UJAIL:-}" 2>/dev/null || true' EXIT INT TERM HUP

run_uninstall_main() {
  # run_uninstall_main <jail> [args...]: child sh, jailed HOME.
  _uj="$1"
  shift
  HOME="$_uj" INSTALL_DIR="" "$TEST_SH" -c 'FORCEFIELD_LIB_ONLY=1 . "$0"; main "$@"' "$UNINSTALL_SH" "$@"
}

ujail_bin() {
  # ujail_bin <version-line>: places a fake ff in the jailed bin dir
  mkdir -p "$UJAIL/bin"
  {
    printf "#!/bin/sh\n"
    printf "printf '%%s\\n' \"%s\"\n" "$1"
    printf "exit 0\n"
  } > "$UJAIL/bin/ff"
  chmod +x "$UJAIL/bin/ff"
}

file_gone() {
  # file_gone <path>: prints 0 when absent, 1 when present (set -e safe)
  if [ -e "$1" ]; then printf "%s" "1"; else printf "%s" "0"; fi
}

set +e
u_out="$(run_uninstall_main "$UJAIL" --dir "$UJAIL/bin" 2>&1)"
u_code=$?
set -e
assert_eq "uninstall absent exits 0" "0" "$u_code"
case "$u_out" in
  *"not installed"*) pass=$((pass + 1)) ;;
  *) fail=$((fail + 1)); printf "FAIL - uninstall reports absent install:\n%s\n" "$u_out" ;;
esac

ujail_bin "ff version v1.2.0"
set +e
u_out="$(run_uninstall_main "$UJAIL" --dir "$UJAIL/bin" 2>&1)"
u_code=$?
set -e
assert_eq "uninstall removes exits 0" "0" "$u_code"
assert_eq "uninstall removes the binary" "0" "$(file_gone "$UJAIL/bin/ff")"
case "$u_out" in
  *"Binary removed"*) pass=$((pass + 1)) ;;
  *) fail=$((fail + 1)); printf "FAIL - uninstall reports removal:\n%s\n" "$u_out" ;;
esac
case "$u_out" in
  *"$ESC"*) fail=$((fail + 1)); printf "FAIL - piped uninstall leaks ANSI:\n%s\n" "$u_out" ;;
  *) pass=$((pass + 1)) ;;
esac

set +e
u_out="$(run_uninstall_main "$UJAIL" --dir "$UJAIL/bin" 2>&1)"
u_code=$?
set -e
assert_eq "uninstall twice stays 0 (idempotent)" "0" "$u_code"
case "$u_out" in
  *"not installed"*) pass=$((pass + 1)) ;;
  *) fail=$((fail + 1)); printf "FAIL - second uninstall reports absent:\n%s\n" "$u_out" ;;
esac

mkdir -p "$UJAIL/.forcefield"
printf "config" > "$UJAIL/.forcefield/config.yaml"
ujail_bin "ff version v1.2.0"
set +e
run_uninstall_main "$UJAIL" --dir "$UJAIL/bin" >/dev/null 2>&1
u_code=$?
set -e
assert_eq "uninstall exits 0 with config present" "0" "$u_code"
assert_eq "uninstall preserves config" "1" "$(file_gone "$UJAIL/.forcefield/config.yaml")"

printf "not a binary" > "$UJAIL/bin/ff"
set +e
u_out="$(run_uninstall_main "$UJAIL" --dir "$UJAIL/bin" 2>&1)"
u_code=$?
set -e
assert_eq "uninstall unreadable binary exits 0" "0" "$u_code"
case "$u_out" in
  *"unknown"*) pass=$((pass + 1)) ;;
  *) fail=$((fail + 1)); printf "FAIL - uninstall reports unknown version:\n%s\n" "$u_out" ;;
esac

set +e
u_out="$(run_uninstall_main "$UJAIL" --dir "$UJAIL/.forcefield" 2>&1)"
u_code=$?
set -e
assert_eq "uninstall refuses inside forcefield home" "1" "$u_code"
case "$u_out" in
  *"Refusing to uninstall"*) pass=$((pass + 1)) ;;
  *) fail=$((fail + 1)); printf "FAIL - uninstall keeps safety refusal:\n%s\n" "$u_out" ;;
esac

# ---------- summary ----------

printf "\n%d passed, %d failed\n" "$pass" "$fail"
if [ "$fail" -ne 0 ]; then
  exit 1
fi
