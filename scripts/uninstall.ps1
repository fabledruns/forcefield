#Requires -Version 5.1
<#
.SYNOPSIS
  Forcefield uninstaller for Windows
.DESCRIPTION
  Removes only the installed ff.exe binary. Never touches ~/.forcefield,
  sessions, memory, or configuration.

  Usage:
    powershell -ExecutionPolicy Bypass -File uninstall.ps1
    uninstall.ps1 -InstallDir "$HOME\.local\bin"
    uninstall.ps1 -RemovePath

  What is removed:
    <InstallDir>\ff.exe

  With -RemovePath (opt-in only), the exact installation directory is
  also removed from the User PATH. Nothing else in PATH is touched,
  System PATH is never modified, and shell profiles are never edited.

  What is NEVER removed:
    ~/.forcefield/config.yaml
    ~/.forcefield/skills/
    ~/.forcefield/.env
    .forcefield/sessions/ (per-project)
#>
[CmdletBinding()]
param(
  [string]$InstallDir = "",
  [switch]$RemovePath,
  [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# ---------- pure behavior + terminal UI (testable) ----------
# These functions have no side effects and are defined outside the guarded
# main flow so tests can dot-source this file with
# $env:FORCEFIELD_LIB_ONLY = '1' and call them directly.
# The UI mirrors install.ps1 (single-file script: must stay
# self-contained). Keep in sync: palette, glyphs, brand, steps.
# Palette mirrors internal/tui/styles.go; glyph code points match
# internal/tui/icons.go. This file stays pure ASCII: WinPS 5.1 misreads
# BOM-less UTF-8 bytes as smart quotes and fails to parse.

$script:UiMode = 'plain+ascii'
$script:UiRuleLen = 40

function Get-UiPalette($uiMode) {
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
  # U+2713 done, U+203A active, U+25CB pending, U+2715 failure, U+2192 arrow.
  if ($uiMode -like '*+unicode') {
    return @{ Ok=[char]0x2713; Step=[char]0x203A; Pend=[char]0x25CB; Fail=[char]0x2715; Warn='!'; Arrow=[char]0x2192 }
  }
  return @{ Ok='*'; Step='>'; Pend='o'; Fail='x'; Warn='!'; Arrow='->' }
}

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
  # modern-terminal signal (COLORTERM) vouches for color support.
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

function Initialize-Ui($uiMode) {
  $script:UiMode = $uiMode
}

function Format-Brand($uiMode) {
  $p = Get-UiPalette $uiMode
  if ($uiMode -like 'color+*') {
    return "$($p.BoldAccent)FORCE$($p.Reset)$($p.Bold)FIELD$($p.Reset)`n$($p.Muted)the local-first agent harness$($p.Reset)"
  }
  return "FORCEFIELD`nthe local-first agent harness"
}

function Format-Rule($uiMode) {
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
  $p = Get-UiPalette $uiMode
  $g = Get-UiGlyphs $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Accent)$($g.Step)$($p.Reset) $label" }
  return "$($g.Step) $label"
}

function Format-Done($uiMode, $label) {
  $p = Get-UiPalette $uiMode
  $g = Get-UiGlyphs $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Success)$($g.Ok)$($p.Reset) $label" }
  return "$($g.Ok) $label"
}

function Format-Warn($uiMode, $msg) {
  $p = Get-UiPalette $uiMode
  $g = Get-UiGlyphs $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Warn)$($g.Warn)$($p.Reset) $msg" }
  return "$($g.Warn) $msg"
}

function Format-Fail($uiMode, $msg) {
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
  return ("  " + "$label".PadRight(12) + " " + "$value")
}

function Format-Detail($uiMode, $msg) {
  $p = Get-UiPalette $uiMode
  if ($uiMode -like 'color+*') { return "$($p.Muted)  $msg$($p.Reset)" }
  return "  $msg"
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

function Fail($msg) {
  Write-Host ""
  foreach ($line in ((Format-Fail $script:UiMode $msg) -split "`n")) {
    Write-Host $line
  }
  throw $msg
}

# Read the installed version from an existing binary by running
# "<path> --version" and parsing the "ff version <tag>" line. Returns
# the tag, or "" when missing, unrunnable, or unrecognizable. Never
# throws: detection problems mean "unknown", never an uninstall error.
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

# Remove exactly one directory from a semicolon-separated PATH string.
# Returns @{ Path = <new string>; Removed = <count> }. Matching is
# case-insensitive and trailing-slash-tolerant; substrings never match;
# all other entries keep their order. Empty inputs are safe no-ops.
function Remove-DirFromPath($pathString, $dir) {
  if (-not $pathString -or -not $dir) {
    if (-not $pathString) { $pathString = "" }
    return @{ Path = "$pathString"; Removed = 0 }
  }
  $want = (Normalize-Dir $dir).ToLower()
  $kept = @()
  $removed = 0
  foreach ($entry in ("$pathString" -split ';')) {
    if ((Normalize-Dir $entry).ToLower() -eq $want) { $removed++ }
    else { $kept += $entry }
  }
  return @{ Path = ($kept -join ';'); Removed = $removed }
}

# ---------- main flow (guarded) ----------
# The uninstaller executes only when the file runs normally. Tests
# dot-source with $env:FORCEFIELD_LIB_ONLY = '1' to load Fail without
# side effects (no filesystem changes).
if ($env:FORCEFIELD_LIB_ONLY -ne '1') {
Initialize-Ui (Get-UiMode)
Write-Brand
Write-Rule
Write-Host ""

if ($Help) { Get-Help $PSCommandPath -Detailed; return }

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

if ($InstallDir -match '^~[\\/]') {
  $homeDir = if ($HOME) { $HOME } else { $env:USERPROFILE }
  $InstallDir = Join-Path $homeDir $InstallDir.Substring(2)
}

# Safety: refuse to operate inside ~/.forcefield
$forcefieldHome = if ($HOME) { Join-Path $HOME ".forcefield" } else { Join-Path $env:USERPROFILE ".forcefield" }
if ($InstallDir -eq $forcefieldHome -or $InstallDir.StartsWith("$forcefieldHome\")) {
  Fail "Refusing to uninstall from inside $forcefieldHome ($InstallDir). This directory holds your config/sessions."
}

$target = Join-Path $InstallDir "ff.exe"
$targetAlt = Join-Path $InstallDir "ff"

Write-Step "Detecting installation"
Write-Row "Location" $target
$installedVersion = Get-InstalledVersion $target
if (-not $installedVersion) { $installedVersion = Get-InstalledVersion $targetAlt }
$anyPresent = (Test-Path $target) -or (Test-Path $targetAlt)
if ($installedVersion) {
  Write-Row "Installed" $installedVersion
} elseif ($anyPresent) {
  Write-Row "Installed" "unknown"
} else {
  Write-Row "Installed" "none"
}

$found = $false
if ($anyPresent) {
  Write-Step "Removing binary"
}
foreach ($candidate in @($target, $targetAlt)) {
  if (Test-Path $candidate) {
    Write-Detail "Removing $candidate"
    try { Remove-Item -Force $candidate -ErrorAction Stop; $found = $true }
    catch { Fail "Failed to remove $candidate : $_" }
  }
}

if (-not $found) {
  Write-Done "Forcefield is not installed"
} else {
  Write-Done "Binary removed"
  Write-Row "Removed" $target
}

# Check still on PATH
$onPath = Get-Command ff -ErrorAction SilentlyContinue
if ($onPath) {
  Write-WarnUi "ff is still found on PATH at: $($onPath.Source)"
  Write-Detail "You may have another installation elsewhere."
} else {
  Write-Done "ff is no longer on PATH"
}

# PATH entry: never touched unless -RemovePath explicitly opts in. Only
# the exact installation directory is removed; every other entry keeps
# its order. System PATH and shell profiles are never modified.
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($RemovePath) {
  $res = Remove-DirFromPath $userPath $InstallDir
  if ($res.Removed -gt 0) {
    [Environment]::SetEnvironmentVariable("Path", $res.Path, "User")
    $env:Path = (Remove-DirFromPath $env:Path $InstallDir).Path
    Write-Done "Removed $InstallDir from User PATH"
  } else {
    Write-Detail "User PATH did not contain the installation directory"
  }
} elseif ((Remove-DirFromPath $userPath $InstallDir).Removed -gt 0) {
  Write-Detail "User PATH still contains $InstallDir (uninstall with -RemovePath to remove it)"
}

Write-Detail "Kept (never removed): config, skills, .env under $forcefieldHome; per-project sessions and memory."
Write-Detail "To fully reset, delete $forcefieldHome manually (config and skills go too; per-project sessions stay)."
if ($found) {
  Write-Done "Forcefield uninstalled"
}
} # end main flow (skipped when dot-sourced with FORCEFIELD_LIB_ONLY=1)
