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

  What is removed:
    <InstallDir>\ff.exe

  What is NEVER removed:
    ~/.forcefield/config.yaml
    ~/.forcefield/skills/
    ~/.forcefield/.env
    .forcefield/sessions/ (per-project)
#>
[CmdletBinding()]
param(
  [string]$InstallDir = "",
  [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Fail($msg) { Write-Host "[ERROR] $msg" -ForegroundColor Red; throw $msg }

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

$found = $false
foreach ($candidate in @($target, $targetAlt)) {
  if (Test-Path $candidate) {
    Write-Host "[INFO] Removing $candidate"
    try { Remove-Item -Force $candidate -ErrorAction Stop; $found = $true }
    catch { Fail "Failed to remove $candidate : $_" }
  }
}

if (-not $found) {
  Write-Host "[INFO] No Forcefield binary found at $target (already removed?)"
} else {
  Write-Host "[INFO] Removed Forcefield binary from $InstallDir"
}

# Check still on PATH
$onPath = Get-Command ff -ErrorAction SilentlyContinue
if ($onPath) {
  Write-Host "[WARN] ff is still found on PATH at: $($onPath.Source)" -ForegroundColor Yellow
  Write-Host "       You may have another installation elsewhere."
} else {
  Write-Host "[INFO] ff is no longer on PATH"
}

# Report PATH entry remains
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -and ($userPath -split ';' | Where-Object { $_.TrimEnd('\','/').ToLower() -eq $InstallDir.TrimEnd('\','/').ToLower() })) {
  Write-Host ""
  Write-Host "[INFO] Your user PATH still contains $InstallDir" -ForegroundColor Yellow
  Write-Host "       The uninstaller does not modify PATH automatically."
  Write-Host "       To remove it manually:"
  Write-Host "         1. Open: System Properties -> Environment Variables"
  Write-Host "         2. Edit 'Path' under User variables"
  Write-Host "         3. Remove the entry: $InstallDir"
  Write-Host "       Or in PowerShell (run as your user, not admin):"
  Write-Host "         `$p = [Environment]::GetEnvironmentVariable('Path','User') -split ';' | Where-Object { `$_ -ne '$InstallDir' }"
  Write-Host "         [Environment]::SetEnvironmentVariable('Path', (`$p -join ';'), 'User')"
}

Write-Host ""
Write-Host "Uninstall complete."
Write-Host "  Removed: $InstallDir\ff.exe (and ff if present)"
Write-Host ""
Write-Host "What remains (never removed by uninstaller):"
Write-Host "  $forcefieldHome\config.yaml"
Write-Host "  $forcefieldHome\skills\"
Write-Host "  $forcefieldHome\.env"
Write-Host "  .forcefield\sessions\   (per-project)"
Write-Host ""
Write-Host "To fully reset Forcefield, delete $forcefieldHome manually if desired."
Write-Host "Note: this deletes your config and skills, but not per-project sessions."
Write-Host ""
