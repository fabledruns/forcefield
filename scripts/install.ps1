#Requires -Version 5.1
<#
.SYNOPSIS
  Forcefield installer for Windows
.DESCRIPTION
  Downloads the appropriate Forcefield release binary, verifies its
  checksum, installs it to a user-local directory, and adds that
  directory to the user's PATH if needed.

  Repository: https://github.com/fabledruns/forcefield

  Quick install (latest):
    irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex

  Pin to a version (environment variable):
    $env:FORCEFIELD_VERSION = "v1.0.0"; irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex

  Pin to a version (parameter, when saved locally):
    powershell -ExecutionPolicy Bypass -File install.ps1 -Version v1.0.0

  Custom install directory:
    $env:FORCEFIELD_INSTALL_DIR = "$HOME\.local\bin"; irm ... | iex
    install.ps1 -InstallDir "$HOME\mybin"

  Reinstall the current version:
    install.ps1 -Force
    ($env:FORCEFIELD_FORCE = "1" works too)

  The installer never requires Administrator privileges, never touches
  system PATH, and never removes ~/.forcefield.

.PARAMETER Version
  Release tag to install, e.g. v1.0.0. Defaults to latest release.

.PARAMETER InstallDir
  Directory to install ff.exe into. Defaults to $HOME\.local\bin

.PARAMETER NoModifyPath
  Do not modify the user PATH.

.PARAMETER Force
  Reinstall even when the requested version is already installed, or when
  the installed version is newer (downgrade). Force never bypasses
  checksum verification, version/platform validation, path safety, or
  atomic replacement.

.PARAMETER Help
  Show help.
#>
[CmdletBinding()]
param(
  [string]$Version = "",
  [string]$InstallDir = "",
  [switch]$NoModifyPath,
  [switch]$Force,
  [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$Repo = "fabledruns/forcefield"
$BinaryName = "ff.exe"

# ----------------------------------------------------------------------------
# Terminal UI (presentation only)
# Installer behavior, errors, paths, downloads and checks remain unchanged.
# Shared Forcefield vocabulary (see internal/tui/icons.go):
#   unicode: check done, angle active, circle pending, cross failure, ! warning
#   ascii:   * done, > active, o pending, x failure, ! warning
# (Glyphs are built from [char] codes below so this file stays pure ASCII:
# Windows PowerShell 5.1 misreads BOM-less UTF-8 bytes as smart quotes and
# fails to parse. Palette mirrors internal/tui/styles.go: accent #ED2663,
# text #EAEAEA, muted #7A7A7A, error #FF6B6B, success #7D9B76,
# warning #C4A35A.)
# Format-* functions are pure (explicit -UiMode) so tests can assert
# rendering invariants; Write-* wrappers use the flow's $script:UiMode.
# ----------------------------------------------------------------------------

# Keep geometric symbols intact on modern Windows terminals (best effort only).
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}

$script:UiMode = 'plain+ascii'
$script:UiRuleLen = 40

function Get-UiPalette($uiMode) {
  # Name -> ANSI escape ('' throughout when plain: never emit ANSI).
  # Depth: truecolor when COLORTERM says so, 256-color approximations of
  # the TUI palette on *256color* TERM, basic red/green/yellow otherwise.
  $p = @{ Accent=''; Text=''; Muted=''; Error=''; Success=''; Warn=''; Reset=''; Bold=''; BoldAccent='' }
  if ($uiMode -notlike 'color+*') { return $p }
  $depth = 'basic'
  if ($env:COLORTERM -match 'truecolor|24bit') { $depth = 'true' }
  elseif ($env:TERM -like '*256color*') { $depth = '256' }
  $e = [char]27
  if ($depth -eq 'true') {
    $p.Accent = "$e[38;2;237;38;99m"
    $p.Text = "$e[38;2;234;234;234m"
    $p.Muted = "$e[38;2;122;122;122m"
    $p.Error = "$e[38;2;255;107;107m"
    $p.Success = "$e[38;2;125;155;118m"
    $p.Warn = "$e[38;2;196;163;90m"
    $p.Reset = "$e[0m"
    $p.Bold = "$e[1m"
    $p.BoldAccent = "$e[1;38;2;237;38;99m"
  } elseif ($depth -eq '256') {
    $p.Accent = "$e[38;5;197m"
    $p.Text = "$e[38;5;254m"
    $p.Muted = "$e[38;5;243m"
    $p.Error = "$e[38;5;203m"
    $p.Success = "$e[38;5;108m"
    $p.Warn = "$e[38;5;179m"
    $p.Reset = "$e[0m"
    $p.Bold = "$e[1m"
    $p.BoldAccent = "$e[1;38;5;197m"
  } else {
    $p.Accent = "$e[31m"
    $p.Error = "$e[31m"
    $p.Success = "$e[32m"
    $p.Warn = "$e[33m"
    $p.Reset = "$e[0m"
    $p.Bold = "$e[1m"
    $p.BoldAccent = "$e[1;31m"
  }
  return $p
}

function Get-UiGlyphs($uiMode) {
  # Code points keep this file pure ASCII (see the block comment above).
  # U+2713 done, U+203A active, U+25CB pending, U+2715 failure, U+2192 arrow.
  if ($uiMode -like '*+unicode') {
    return @{ Ok=[char]0x2713; Step=[char]0x203A; Pend=[char]0x25CB; Fail=[char]0x2715; Warn='!'; Arrow=[char]0x2192 }
  }
  return @{ Ok='*'; Step='>'; Pend='o'; Fail='x'; Warn='!'; Arrow='->' }
}

function Initialize-Ui($uiMode) {
  $script:UiMode = $uiMode
}

function Format-Brand($uiMode) {
  # Compact Forcefield brand: FORCE in the accent, FIELD in white, plus
  # the tagline. Plain mode prints the same words without decoration.
  $p = Get-UiPalette $uiMode
  if ($uiMode -like 'color+*') {
    return "$($p.BoldAccent)FORCE$($p.Reset)$($p.Bold)FIELD$($p.Reset)`n$($p.Muted)the local-first agent harness$($p.Reset)"
  }
  return "FORCEFIELD`nthe local-first agent harness"
}

function Format-Rule($uiMode) {
  # Thin fixed-width separator. Fixed length keeps piped output
  # deterministic (no width probing).
  $g = Get-UiGlyphs $uiMode
  $ch = '-'
  if ($g.Step -eq ([char]0x203A)) { $ch = [char]0x2500 }
  $line = ''
  for ($i = 0; $i -lt $script:UiRuleLen; $i++) { $line += $ch }
  $p = Get-UiPalette $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Muted)$line$($p.Reset)" }
  return $line
}

function Format-Step($uiMode, $label) {
  # Active step: "> Label" in ascii, angle-quoted in unicode.
  $p = Get-UiPalette $uiMode
  $g = Get-UiGlyphs $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Accent)$($g.Step)$($p.Reset) $label" }
  return "$($g.Step) $label"
}

function Format-Done($uiMode, $label) {
  # Completed step.
  $p = Get-UiPalette $uiMode
  $g = Get-UiGlyphs $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Success)$($g.Ok)$($p.Reset) $label" }
  return "$($g.Ok) $label"
}

function Format-Warn($uiMode, $msg) {
  # Recoverable warning: ! message
  $p = Get-UiPalette $uiMode
  $g = Get-UiGlyphs $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Warn)$($g.Warn)$($p.Reset) $msg" }
  return "$($g.Warn) $msg"
}

function Format-Fail($uiMode, $msg) {
  # Fatal error text: failure glyph plus first line, indented continuation.
  # throw/exit; this only formats. Message text is preserved verbatim.
  $p = Get-UiPalette $uiMode
  $g = Get-UiGlyphs $uiMode
  $lines = "$msg" -split "`n"
  if ($uiMode -like 'color+*') {
    $out = "$($p.Error)$($g.Fail)$($p.Reset) $($lines[0])"
  } else {
    $out = "$($g.Fail) $($lines[0])"
  }
  for ($i = 1; $i -lt $lines.Count; $i++) {
    $out += "`n  $($lines[$i])"
  }
  return $out
}

function Format-Row($label, $value) {
  # Aligned label/value detail: two-space indent, padded label.
  return ("  " + "$label".PadRight(12) + " " + "$value")
}

function Format-Detail($uiMode, $msg) {
  # Muted detail line.
  $p = Get-UiPalette $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Muted)  $msg$($p.Reset)" }
  return "  $msg"
}

function Format-Pending($uiMode, $msg) {
  # Pending user action.
  $g = Get-UiGlyphs $uiMode
  return "$($g.Pend) $msg"
}

function Write-Brand {
  Write-Host (Format-Brand $script:UiMode)
}

function Write-Rule {
  Write-Host (Format-Rule $script:UiMode)
}

function Write-Step($label) {
  Write-Host (Format-Step $script:UiMode $label)
}

function Write-Done($label) {
  Write-Host (Format-Done $script:UiMode $label)
}

function Write-Row($label, $value) {
  Write-Host (Format-Row $label $value)
}

function Write-Detail($msg) {
  Write-Host (Format-Detail $script:UiMode $msg)
}

function Write-WarnUi($msg) {
  Write-Host (Format-Warn $script:UiMode $msg)
}

function Write-Warn($msg) {
  Write-Host (Format-Warn $script:UiMode $msg)
}

function Fail($msg) {
  Write-Host ""
  foreach ($line in ((Format-Fail $script:UiMode $msg) -split "`n")) {
    Write-Host $line
  }
  throw $msg
}

# ---------- pure behavior (testable) ----------
# These functions have no side effects and are defined outside the guarded
# main flow so tests can dot-source this file with
# $env:FORCEFIELD_LIB_ONLY = '1' and call them directly.

# Validate version format if provided
function Test-VersionFormat($v) {
  if (-not $v -or $v.Trim() -eq "") { return }
  if ($v -match '\.\.' -or $v -match '/' -or $v -match '\\' -or $v -match ';' -or $v -match '&' -or $v -match '\|' -or $v -match '`' -or $v -match '\$' -or $v -match ' ') {
    Fail "Invalid version (contains forbidden characters): $v"
  }
  if ($v -notmatch '^[vV]?[0-9][A-Za-z0-9._+\-]*$') {
    Fail "Invalid version format: $v (expected vX.Y.Z)"
  }
}

# Strip the leading v/V prefix and any +build metadata, so
# "v1.2.0-rc.1+001" becomes "1.2.0-rc.1". Pure string handling;
# validation stays in Test-VersionFormat.
function Get-NormalizedVersion($v) {
  if (-not $v) { return "" }
  $s = "$v"
  if ($s.StartsWith("v") -or $s.StartsWith("V")) { $s = $s.Substring(1) }
  $plus = $s.IndexOf("+")
  if ($plus -ge 0) { $s = $s.Substring(0, $plus) }
  return $s
}

# Compare two versions after normalization. Returns -1, 0, or 1
# (a older, equal, a newer). Numeric fields compare numerically
# ("1.2.10" beats "1.2.3"); missing fields count as zero; a release
# beats its own prerelease; prerelease fields compare numerically when
# both are numeric, lexically otherwise.
function Compare-VersionFields($x, $y) {
  $xs = @()
  $ys = @()
  if ($x -ne $null -and "$x" -ne "") { $xs = "$x" -split '\.' }
  if ($y -ne $null -and "$y" -ne "") { $ys = "$y" -split '\.' }
  $n = $xs.Count
  if ($ys.Count -gt $n) { $n = $ys.Count }
  for ($i = 0; $i -lt $n; $i++) {
    $xf = "0"
    $yf = "0"
    if ($i -lt $xs.Count -and $xs[$i] -ne "") { $xf = $xs[$i] }
    if ($i -lt $ys.Count -and $ys[$i] -ne "") { $yf = $ys[$i] }
    $xn = 0
    $yn = 0
    $xIsNum = [int]::TryParse($xf, [ref]$xn)
    $yIsNum = [int]::TryParse($yf, [ref]$yn)
    if ($xIsNum -and $yIsNum) {
      if ($xn -lt $yn) { return -1 }
      if ($xn -gt $yn) { return 1 }
    } else {
      $c = [string]::Compare($xf, $yf, [System.StringComparison]::Ordinal)
      if ($c -lt 0) { return -1 }
      if ($c -gt 0) { return 1 }
    }
  }
  return 0
}

function Compare-Versions($a, $b) {
  $na = Get-NormalizedVersion $a
  $nb = Get-NormalizedVersion $b
  $aPre = ""
  $bPre = ""
  $aBase = $na
  $bBase = $nb
  $dash = $na.IndexOf("-")
  if ($dash -ge 0) { $aBase = $na.Substring(0, $dash); $aPre = $na.Substring($dash + 1) }
  $dash = $nb.IndexOf("-")
  if ($dash -ge 0) { $bBase = $nb.Substring(0, $dash); $bPre = $nb.Substring($dash + 1) }

  $r = Compare-VersionFields $aBase $bBase
  if ($r -ne 0) { return $r }
  if ($aPre -eq $bPre) { return 0 }
  if ($aPre -eq "") { return 1 }
  if ($bPre -eq "") { return -1 }
  return (Compare-VersionFields $aPre $bPre)
}

# Read the installed version from an existing binary by running
# "<path> --version" and parsing the "ff version <tag>" line. Returns
# the tag ("v1.2.3", or "dev" for local builds), or "" when the binary
# is missing, unrunnable, or its output is unrecognizable. Never throws:
# detection problems mean "unknown", never an install error.
function Get-InstalledVersion($path) {
  if (-not $path -or -not (Test-Path $path)) { return "" }
  try {
    $out = & $path --version 2>&1 | Out-String
  } catch {
    return ""
  }
  $first = "$out" -split "`r?`n" | Select-Object -First 1
  $parts = ($first.Trim() -split '\s+')
  if ($parts.Count -lt 3) { return "" }
  $ver = $parts[2]
  if ($ver -notmatch '^[A-Za-z0-9._+\-]+$') { return "" }
  return $ver
}

# Decide what an install run should do. Returns exactly one of:
# fresh (nothing installed), upgrade (installed older or unknown),
# current (installed equals target), reinstall (equal/newer plus force),
# refuse-downgrade (installed newer, no force).
function Get-InstallAction($installed, $target, $force, $exists) {
  if (-not $exists) { return "fresh" }
  if ("$installed" -notmatch '^[vV]?[0-9]') {
    # Unknown version (dev builds, unparsable output): today's behavior
    # overwrites in place, so treat as upgrade.
    return "upgrade"
  }
  $cmp = Compare-Versions $installed $target
  if ($cmp -lt 0) { return "upgrade" }
  if ($cmp -eq 0) {
    if ($force) { return "reinstall" } else { return "current" }
  }
  if ($force) { return "reinstall" } else { return "refuse-downgrade" }
}

# Detect the existing installation (if any) at one binary path and decide
# the action for target $target with $force. Detection never throws.
function Get-ExistingInstallAction($target, $force, $binaryPath) {
  $exists = $false
  if ($binaryPath -and (Test-Path $binaryPath)) { $exists = $true }
  $installed = ""
  if ($exists) { $installed = Get-InstalledVersion $binaryPath }
  return (Get-InstallAction $installed $target $force $exists)
}

# The user-local default install directory ("$HOME\.local\bin").
# Returns "" when no home directory is known; callers fail with guidance.
function Get-DefaultInstallDir {
  if ($HOME) { return (Join-Path $HOME ".local\bin") }
  if ($env:USERPROFILE) { return (Join-Path $env:USERPROFILE ".local\bin") }
  return ""
}

# Strip trailing slashes ("C:\opt\ff\" -> "C:\opt\ff"), keeping drive
# roots ("C:\") and "/" intact. "" stays "".
function Normalize-Dir($d) {
  if (-not $d) { return "" }
  $s = "$d"
  while ($s.Length -gt 1 -and ($s.EndsWith('\') -or $s.EndsWith('/'))) {
    if ($s -match '^[A-Za-z]:[\\/]?$') { break }
    $s = $s.Substring(0, $s.Length - 1)
  }
  return $s
}

# Case-insensitive, trailing-slash-tolerant membership test of one
# directory in a semicolon-separated PATH string. Substrings never match.
function Test-DirInPath($pathString, $dir) {
  if (-not $pathString -or -not $dir) { return $false }
  $want = (Normalize-Dir $dir).ToLower()
  foreach ($entry in ("$pathString" -split ';')) {
    if ((Normalize-Dir $entry).ToLower() -eq $want) { return $true }
  }
  return $false
}

# Decide the terminal output mode. Returns "<color|plain>+<unicode|ascii>":
# color only on a real TTY without NO_COLOR and with a capable TERM;
# unicode only with UTF-8 evidence (or forced off via FORCEFIELD_ASCII).
# FORCEFIELD_TTY=0/1 overrides TTY probing (useful for tests and CI).
# No cursor movement, no animation, no width dependence.
function Get-UiMode {
  $tty = $false
  if ($env:FORCEFIELD_TTY -eq '1') { $tty = $true }
  elseif ($env:FORCEFIELD_TTY -eq '0') { $tty = $false }
  else {
    try { $tty = -not [Console]::IsOutputRedirected } catch { $tty = $false }
  }

  $color = $true
  if ($null -ne $env:FORCEFIELD_NO_COLOR -or $null -ne $env:NO_COLOR) { $color = $false }
  # An empty TERM means "unknown", not "dumb": stay plain unless another
  # modern-terminal signal (COLORTERM) vouches for color support. Windows
  # consoles commonly leave TERM unset while setting COLORTERM.
  if ($env:TERM -eq 'dumb') { $color = $false }
  elseif (-not $env:TERM -and -not $env:COLORTERM) { $color = $false }
  if (-not $tty) { $color = $false }

  $glyphs = 'ascii'
  $loc = $env:LC_ALL
  if (-not $loc) { $loc = $env:LC_CTYPE }
  if (-not $loc) { $loc = $env:LANG }
  if ($loc -match '(?i)utf-?8') { $glyphs = 'unicode' }
  if ($env:FORCEFIELD_ASCII -eq '1') { $glyphs = 'ascii' }
  # A UTF-8 locale claim is not enough on its own: consoles that cannot
  # emit UTF-8 (legacy code pages) would mangle the glyphs, so require a
  # UTF-8 output encoding as well.
  if ($glyphs -eq 'unicode') {
    try {
      if ([Console]::OutputEncoding.WebName -ne 'utf-8') { $glyphs = 'ascii' }
    } catch { $glyphs = 'ascii' }
  }

  $c = 'plain'
  if ($color) { $c = 'color' }
  return "$c+$glyphs"
}

# Detect architecture
function Get-Arch {
  $arch = $null
  try {
    $riArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    switch ($riArch) {
      "X64"   { return "amd64" }
      "Arm64" { return "arm64" }
      "Arm"   { Fail "Unsupported architecture: ARM 32-bit is not supported. Supported: amd64, arm64. Manual download: https://github.com/$Repo/releases" }
      default { $arch = $riArch }
    }
  } catch { }

  # Fallback to env vars
  $procArch = $env:PROCESSOR_ARCHITECTURE
  if ($procArch) {
    switch ($procArch.ToUpperInvariant()) {
      "AMD64" { return "amd64" }
      "ARM64" { return "arm64" }
      "X86"   { Fail "Unsupported architecture: x86 32-bit is not supported. Supported: amd64, arm64. Manual download: https://github.com/$Repo/releases" }
      "IA64"  { Fail "Unsupported architecture: IA64 is not supported. Supported: amd64, arm64." }
    }
  }

  # Last resort: check PROCESSOR_ARCHITEW6432 (WOW64)
  $wowArch = $env:PROCESSOR_ARCHITEW6432
  if ($wowArch -and $wowArch.ToUpperInvariant() -eq "AMD64") { return "amd64" }
  if ($wowArch -and $wowArch.ToUpperInvariant() -eq "ARM64") { return "arm64" }

  Fail "Could not detect architecture (OSArchitecture=$riArch, PROCESSOR_ARCHITECTURE=$procArch). Supported: amd64, arm64."
}

function Get-LatestVersion {
  $headers = @{ "Accept" = "application/vnd.github.v3+json"; "User-Agent" = "forcefield-installer" }
  $latestUrl = "https://api.github.com/repos/$Repo/releases/latest"
  $listUrl   = "https://api.github.com/repos/$Repo/releases?per_page=20"

  foreach ($url in @($latestUrl, $listUrl)) {
    try {
      $resp = Invoke-RestMethod -Uri $url -Headers $headers -UseBasicParsing -ErrorAction Stop
      # list endpoint returns array; prefer stable releases
      if ($resp -is [Array]) {
        foreach ($r in $resp) {
          if ($r -and -not $r.prerelease -and $r.tag_name) { return $r.tag_name }
        }
        # No stable found, return first prerelease if any
        if ($resp.Count -gt 0 -and $resp[0].tag_name) { return $resp[0].tag_name }
      } else {
        if ($resp.tag_name) { return $resp.tag_name }
      }
    } catch {
      $status = $null
      try { $status = $_.Exception.Response.StatusCode.value__ } catch {}
      if ($status -eq 404) {
        # latest may 404 if only prereleases exist; try next URL
        continue
      }
      # For rate limiting or network errors, try next URL once then fail
      if ($url -eq $latestUrl) { continue }
      throw
    }
  }
  return $null
}

# ---------- main flow (guarded) ----------
# The installer executes only when the file runs normally. Tests dot-source
# with $env:FORCEFIELD_LIB_ONLY = '1' to load the functions above without
# side effects (no network, no filesystem changes).
if ($env:FORCEFIELD_LIB_ONLY -ne '1') {
Initialize-Ui (Get-UiMode)
Write-Brand
Write-Rule
Write-Host ""

if ($Help) {
  Get-Help $PSCommandPath -Detailed
  return
}

# Reject explicitly passed empty version/dir (distinguish from not passed)
if ($PSBoundParameters.ContainsKey('Version') -and [string]::IsNullOrWhiteSpace($Version)) {
  Fail "--version requires non-empty argument"
}
if ($PSBoundParameters.ContainsKey('InstallDir') -and [string]::IsNullOrWhiteSpace($InstallDir)) {
  Fail "--dir requires non-empty argument"
}

# Resolve install dir -- precedence: param > env > default
if (-not $InstallDir -or $InstallDir.Trim() -eq "") {
  if ($env:FORCEFIELD_INSTALL_DIR -and $env:FORCEFIELD_INSTALL_DIR.Trim() -ne "") {
    $InstallDir = $env:FORCEFIELD_INSTALL_DIR
  } elseif ($HOME) {
    $InstallDir = Join-Path $HOME ".local\bin"
  } elseif ($env:USERPROFILE) {
    $InstallDir = Join-Path $env:USERPROFILE ".local\bin"
  } else {
    Fail "Cannot determine home directory. Pass -InstallDir <path>."
  }
}

# Resolve version -- precedence: param > env > latest
if (-not $Version -or $Version.Trim() -eq "") {
  if ($env:FORCEFIELD_VERSION -and $env:FORCEFIELD_VERSION.Trim() -ne "") {
    $Version = $env:FORCEFIELD_VERSION
  }
}

# Expand leading ~/ if present
if ($InstallDir -match '^~[\\/]') {
  $homeDir = if ($HOME) { $HOME } else { $env:USERPROFILE }
  $InstallDir = Join-Path $homeDir $InstallDir.Substring(2)
} elseif ($InstallDir -eq "~") {
  $InstallDir = if ($HOME) { $HOME } else { $env:USERPROFILE }
}

# Validate install dir is not empty and not inside repo temp etc
if (-not $InstallDir -or $InstallDir.Trim() -eq "") { Fail "Install dir is empty." }
if ($InstallDir.Contains(";") -or $InstallDir.Contains("`n") -or $InstallDir.Contains("`r") -or $InstallDir.Contains("`t")) {
  Fail "Install dir contains invalid characters (;, newline, tab): $InstallDir"
}
# On Windows, colon is allowed only as drive separator (e.g., C:\), but not as extra delimiter.
# Allow single colon at position 1 for drive letter, but not elsewhere.
if ($InstallDir -match '^[A-Za-z]:\\') {
  $rest = $InstallDir.Substring(2)
  if ($rest.Contains(":") -or $rest.Contains(";")) { Fail "Install dir contains PATH delimiter: $InstallDir" }
} elseif ($InstallDir.Contains(":") -or $InstallDir.Contains(";")) {
  Fail "Install dir contains PATH delimiter: $InstallDir"
}
# Warn if inside ~/.forcefield (sessions/config) -- allowed but unusual
$forcefieldHome = if ($HOME) { Join-Path $HOME ".forcefield" } else { Join-Path $env:USERPROFILE ".forcefield" }
if ($InstallDir -eq $forcefieldHome -or $InstallDir.StartsWith("$forcefieldHome\")) {
  Write-Warn "Install dir is inside $forcefieldHome; sessions/config live there. This is allowed but unusual."
}

Test-VersionFormat $Version

# Detect architecture

Write-Step "Detecting platform"
$Arch = Get-Arch
Write-Row "Platform" "windows/$Arch"
Write-Verbose "platform: windows/$Arch"

# Resolve latest version if not specified

if (-not $Version -or $Version.Trim() -eq "") {
  Write-Step "Resolving latest release"
  Write-Verbose "resolving latest release via GitHub API"
  $Version = Get-LatestVersion
  if (-not $Version) {
    Fail "Could not determine latest release. GitHub API may be rate-limited or offline. Try: install.ps1 -Version v1.0.0 or set `$env:FORCEFIELD_VERSION='v1.0.0'. Manual: https://github.com/$Repo/releases"
  }
  Write-Done "Release $Version"
} else {
  if ($Version -notmatch '^[vV]') { $Version = "v$Version" }
}
Test-VersionFormat $Version
Write-Verbose "release: $Version"

$Artifact = "ff-windows-$Arch.exe"
$DownloadUrl = "https://github.com/$Repo/releases/download/$Version/$Artifact"
$ChecksumUrl = "https://github.com/$Repo/releases/download/$Version/checksums.txt"

Write-Verbose "artifact: $Artifact"
Write-Verbose "download: $DownloadUrl"
Write-Verbose "checksums: $ChecksumUrl"

# Resolve force: explicit switch wins, environment opts in.
if ($env:FORCEFIELD_FORCE -eq '1') { $Force = $true }

# Existing installation: decide before any download. Already-current
# installs exit early without touching the network artifact or the
# binary; downgrades refuse unless forced. Force only bypasses these two
# gates: verification, platform, path safety, and atomic replacement
# below always apply.
$target = Join-Path $InstallDir $BinaryName
$installAction = Get-ExistingInstallAction $Version ([bool]$Force) $target
switch ($installAction) {
  "current" {
    Write-Done "Forcefield $Version is already installed"
    Write-Row "Location" $target
    if (Test-DirInPath $env:Path $InstallDir) {
      Write-Row "PATH" "configured"
    } else {
      Write-Row "PATH" "missing"
      Write-WarnUi "PATH does not contain $InstallDir. Add it manually or re-run with -Force to reinstall."
    }
    return
  }
  "refuse-downgrade" {
    $installedNow = Get-InstalledVersion $target
    if (-not $installedNow) { $installedNow = "unknown" }
    Fail "Installed version $installedNow is newer than $Version; refusing to downgrade. Pass -Force to reinstall anyway."
  }
}

# Installation plan: what is about to happen, before any download.
Write-Step "Installation plan"
Write-Row "Version" $Version
$arrow = (Get-UiGlyphs $script:UiMode).Arrow
switch ($installAction) {
  "fresh" { Write-Row "Action" "Fresh install" }
  "upgrade" {
    $from = Get-InstalledVersion $target
    if ($from) { Write-Row "Action" "Upgrade $from $arrow $Version" }
    else { Write-Row "Action" "Upgrade" }
  }
  default { Write-Row "Action" "Reinstall $Version" }
}
Write-Row "Location" $target
if (Test-DirInPath $env:Path $InstallDir) {
  Write-Row "PATH" "configured"
} elseif ($NoModifyPath) {
  Write-Row "PATH" "will not be modified"
} else {
  Write-Row "PATH" "will be configured"
}

# Create temp dir
$tmpRoot = [System.IO.Path]::GetTempPath()
$tmp = Join-Path $tmpRoot "forcefield-install-$(Get-Random)-$PID"
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
# Ensure cleanup on exit
$cleanup = {
  try { if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue } } catch {}
}
# Suppress Invoke-WebRequest progress bars (no redrawing meters in
# install output); restored in the finally block below.
$savedProgressPreference = $ProgressPreference
$ProgressPreference = 'SilentlyContinue'
# Use try/finally for main logic; also register engine exit
try {
  # Download artifact
  $dest = Join-Path $tmp $Artifact
  Write-Step "Downloading $Artifact $Version"
  Write-Verbose "downloading $DownloadUrl to $dest"
  try {
    # Use Invoke-WebRequest with UseBasicParsing for PS5 compatibility
    Invoke-WebRequest -Uri $DownloadUrl -OutFile $dest -UseBasicParsing -Headers @{"User-Agent"="forcefield-installer"} -ErrorAction Stop
  } catch {
    $status = $null
    try { $status = $_.Exception.Response.StatusCode.value__ } catch {}
    if ($status -eq 404) {
      Fail "Download failed (404): $DownloadUrl`nThe artifact may not exist for windows/$Arch at $Version.`nCheck https://github.com/$Repo/releases/tag/$Version and supported architectures: windows/amd64, windows/arm64"
    }
    Fail "Download failed: $DownloadUrl`n$_`nCheck your network and try again. Manual download: https://github.com/$Repo/releases/tag/$Version"
  }

  if (-not (Test-Path $dest) -or (Get-Item $dest).Length -eq 0) {
    Fail "Downloaded file is empty: $dest (check $DownloadUrl)"
  }
  Write-Done "Download complete"

  # Verify checksum
  Write-Step "Verifying checksum"
  $checksumFile = Join-Path $tmp "checksums.txt"
  $checksumOk = $false
  $foundChecksumUrl = $null
  foreach ($url in @($ChecksumUrl, "https://github.com/$Repo/releases/download/$Version/SHA256SUMS", "https://github.com/$Repo/releases/download/$Version/checksums.sha256")) {
    try {
      Invoke-WebRequest -Uri $url -OutFile $checksumFile -UseBasicParsing -Headers @{"User-Agent"="forcefield-installer"} -ErrorAction Stop
      if ((Test-Path $checksumFile) -and (Get-Item $checksumFile).Length -gt 0) {
        $foundChecksumUrl = $url
        Write-Verbose "checksum source: $url"
        break
      }
    } catch {
      # try next URL on 404
      try { $s = $_.Exception.Response.StatusCode.value__ } catch { $s = $null }
      if ($s -eq 404) { continue }
      # ignore other errors for checksum (will warn)
      Remove-Item $checksumFile -Force -ErrorAction SilentlyContinue
      continue
    }
    Remove-Item $checksumFile -Force -ErrorAction SilentlyContinue
  }

  if ($foundChecksumUrl -and (Test-Path $checksumFile)) {
    $lines = Get-Content $checksumFile -ErrorAction SilentlyContinue
    $expected = $null
    foreach ($line in $lines) {
      $trimmed = $line.Trim()
      if (-not $trimmed) { continue }
      $parts = $trimmed -split '\s+'
      if ($parts.Count -lt 2) { continue }
      $hash = $parts[0].Trim().ToLower()
      if ($hash -notmatch '^[a-f0-9]{64}$') { continue }
      $file = $parts[1].Trim().TrimStart('*').TrimEnd("`r")
      if ($file -ne $Artifact) { continue }
      $expected = $hash
      break
    }
    if (-not $expected) {
      Write-WarnUi "checksums.txt does not contain $Artifact; skipping verification (this should not happen for new releases)"
    } else {
      $actualHash = (Get-FileHash -Path $dest -Algorithm SHA256).Hash.ToLower()
      if ($expected -ne $actualHash) {
        Fail "Checksum mismatch for $Artifact`n  expected: $expected`n  actual:   $actualHash`nRefusing to install. The download may be corrupted or tampered with."
      }
      Write-Done "Checksum verified"
      $checksumOk = $true
    }
  } else {
    Write-WarnUi "no checksums file; skipping verification (old releases may not have checksums)"
    Write-Detail "For security, prefer a release with checksums.txt."
    Write-Verbose "No checksums file at $ChecksumUrl (old releases may not have checksums)"
  }

  # Install
  Write-Step "Installing to $InstallDir"
  Write-Verbose "installing to $InstallDir"
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

  $isUpgrade = Test-Path $target
  if ($isUpgrade) {
    Write-Verbose "existing installation found, upgrading in place"
  } else {
    Write-Verbose "new installation"
  }

  # Copy atomically: copy to temp name then move (same directory = atomic on same volume)
  $tmpTarget = Join-Path $InstallDir "ff.exe.tmp"
  # Stale temp from previous interrupted run
  if (Test-Path $tmpTarget) { Remove-Item $tmpTarget -Force -ErrorAction SilentlyContinue }
  Copy-Item -Path $dest -Destination $tmpTarget -Force
  try {
    Move-Item -Path $tmpTarget -Destination $target -Force -ErrorAction Stop
  } catch {
    # Move failed (e.g., file in use) - clean up staging file and rethrow
    try { if (Test-Path $tmpTarget) { Remove-Item $tmpTarget -Force -ErrorAction SilentlyContinue } } catch {}
    Fail "Failed to install runtime`n$_"
  }
  Write-Verbose "installed $target"
  if ($isUpgrade) {
    Write-Done "Installed $target (upgraded)"
  } else {
    Write-Done "Installed $target"
  }

  # Verify
  Write-Step "Verifying installation"
  $installedVer = $null
  try {
    $out = & $target --version 2>&1 | Out-String
    $installedVer = $out.Trim().Split("`n")[0].Trim()
    if ($installedVer) { Write-Done "Verified: $installedVer" }
    # Reset exit code: external --version on older binaries returns 1, but installer should not fail
    $global:LASTEXITCODE = 0
  } catch {
    Write-WarnUi "Installed binary does not support --version; checking --help instead"
    try { & $target --help 2>&1 | Out-Null; $global:LASTEXITCODE = 0 } catch {
      Write-WarnUi "Installed binary failed to run --help; it may be the wrong architecture"
      $global:LASTEXITCODE = 0
    }
    $installedVer = "(unknown, use ff --version after fixing PATH)"
    $global:LASTEXITCODE = 0
  }

  # PATH handling
  $needsPathUpdate = $false
  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
  if (-not $userPath) { $userPath = "" }
  $pathEntries = $userPath -split ';' | Where-Object { $_ -ne "" } | ForEach-Object { $_.TrimEnd('\','/') }
  $installDirNorm = $InstallDir.TrimEnd('\','/')

  $alreadyInPath = $false
  foreach ($p in $pathEntries) {
    if ($p.TrimEnd('\','/').ToLower() -eq $installDirNorm.ToLower()) { $alreadyInPath = $true; break }
  }
  # Also check current process PATH
  $procAlreadyInPath = $false
  if ($env:Path -split ';' | Where-Object { $_.TrimEnd('\','/').ToLower() -eq $installDirNorm.ToLower() }) {
    $procAlreadyInPath = $true
  }

  Write-Verbose "install dir: $InstallDir"
  if ($alreadyInPath) {
    Write-Done "PATH already contains $InstallDir"
  } else {
    Write-Step "Configuring PATH"
    $needsPathUpdate = $true
    if ($NoModifyPath) {
      Write-WarnUi "PATH update required"
      Write-Detail "PATH does not contain $InstallDir (path modification disabled via -NoModifyPath)"
    } else {
      Write-Verbose "updating user PATH"
      if ($userPath -and $userPath.Trim() -ne "") {
        $newPath = "$userPath;$InstallDir"
      } else {
        $newPath = $InstallDir
      }
      # Avoid duplicate: ensure not already present (case-insensitive)
      [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
      Write-Done "PATH ready"
      # Also update current session
      if (-not $procAlreadyInPath) {
        $env:Path = "$env:Path;$InstallDir"
      }
      Write-Verbose "current terminal PATH updated"
    }
  }

  # Final output
  Write-Verbose "binary: $target"
  Write-Verbose "install dir: $InstallDir"
  if ($installedVer) { Write-Verbose "version output: $installedVer" }
  Write-Verbose "github.com/$Repo"
  Write-Host ""
  Write-Rule
  Write-Done "Forcefield $Version installed"
  Write-Row "Location" $target
  if ($needsPathUpdate) {
    if (-not $NoModifyPath -and (Test-DirInPath $env:Path $InstallDir)) {
      # The current session already picked it up above; other terminals
      # need a restart.
      Write-Detail "Added to user PATH. Restart your terminal, or run:"
      Write-Detail "`$env:Path += `";$InstallDir`""
    } elseif (-not $NoModifyPath) {
      Write-WarnUi "$InstallDir is not in your current PATH."
      Write-Detail "Added to user PATH. Restart your terminal, or run:"
      Write-Detail "`$env:Path += `";$InstallDir`""
    } else {
      Write-WarnUi "$InstallDir is not in your current PATH."
      Write-Detail "Add it to your user PATH (not system):"
      Write-Detail "[Environment]::SetEnvironmentVariable('Path', `"`$env:Path;$InstallDir`", 'User')"
    }
  } else {
    Write-Row "PATH" "configured"
  }
  Write-Host (Format-Pending $script:UiMode "Next: ff doctor")

  if (-not $checksumOk) {
    Write-Detail "Note: checksum verification was skipped or unavailable. For production, use a release with checksums.txt."
  }

  $global:LASTEXITCODE = 0
} finally {
  & $cleanup
  try { if ($tmpTarget -and (Test-Path $tmpTarget)) { Remove-Item $tmpTarget -Force -ErrorAction SilentlyContinue } } catch {}
  $ProgressPreference = $savedProgressPreference
  # Ensure the installer itself exits 0 on success, even if subprocess set LASTEXITCODE
  if ($global:LASTEXITCODE -ne 0 -and $Error.Count -eq 0) { $global:LASTEXITCODE = 0 }
}
} # end main flow (skipped when dot-sourced with FORCEFIELD_LIB_ONLY=1)
