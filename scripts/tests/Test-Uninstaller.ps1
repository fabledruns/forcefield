#Requires -Version 5.1
<#
.SYNOPSIS
  Behavior tests for scripts/uninstall.ps1. No network, no side effects
  outside temporary directories (User PATH is never touched: -RemovePath
  wiring is covered through the pure Remove-DirFromPath function).
.DESCRIPTION
  Run from the repository root:
    pwsh -NoProfile -File scripts/tests/Test-Uninstaller.ps1
    powershell -NoProfile -ExecutionPolicy Bypass -File scripts/tests/Test-Uninstaller.ps1

  The uninstaller is dot-sourced with $env:FORCEFIELD_LIB_ONLY = '1',
  which loads its functions without executing anything. Hand-rolled
  asserts, no framework (works on PowerShell 7 and Windows PowerShell 5.1).
#>
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$script:pass = 0
$script:fail = 0

function Assert-Equal($desc, $expected, $actual) {
  if ("$expected" -eq "$actual") {
    $script:pass++
  } else {
    $script:fail++
    Write-Host "FAIL - $desc"
    Write-Host "  expected: <$expected>"
    Write-Host "  actual:   <$actual>"
  }
}

function Assert-True($desc, $cond) {
  if ($cond) {
    $script:pass++
  } else {
    $script:fail++
    Write-Host "FAIL - $desc"
  }
}

function Invoke-Child($exe, $arguments) {
  # Runs a child process with native stderr neutralized (WinPS 5.1 would
  # otherwise terminate under $ErrorActionPreference = 'Stop').
  # Returns @{ Output = <string>; Code = <int> }.
  # NOTE: the parameter is named $arguments, not $args: $args is an
  # automatic variable and would shadow the caller's argument list.
  $savedEap = $ErrorActionPreference
  $ErrorActionPreference = 'Continue'
  try {
    $raw = & $exe $arguments 2>&1
    $code = $LASTEXITCODE
    return @{ Output = ($raw | Out-String); Code = $code }
  } finally {
    $ErrorActionPreference = $savedEap
  }
}

$uninstallerPath = Join-Path (Join-Path $PSScriptRoot "..") "uninstall.ps1"

# ---------- regression: importing must not execute ----------

function Test-LibOnlyImportIsSilent {
  $exe = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
  $cmd = '$env:FORCEFIELD_LIB_ONLY=''1''; . ''' + $uninstallerPath + ''''
  $r = Invoke-Child $exe @('-NoProfile', '-Command', $cmd)
  Assert-Equal "lib-only uninstall import exit code" 0 $r.Code
  Assert-Equal "lib-only uninstall import produces no output" "" $r.Output.Trim()
}

# ---------- load functions for the behavior tests below ----------

$env:FORCEFIELD_LIB_ONLY = '1'
. $uninstallerPath
Remove-Item Env:\FORCEFIELD_LIB_ONLY -ErrorAction SilentlyContinue

# ---------- -RemovePath pure behavior ----------

function Test-RemoveDirFromPath {
  $r = Remove-DirFromPath "C:\a;C:\b" "c:\b"
  Assert-Equal "exact entry removed" "C:\a" $r.Path
  Assert-Equal "removal counted" 1 $r.Removed

  $r = Remove-DirFromPath "C:\OPT\FF;C:\bin" "c:\opt\ff\"
  Assert-Equal "match is case-insensitive and slash-tolerant" "C:\bin" $r.Path
  Assert-Equal "removal counted once" 1 $r.Removed

  $r = Remove-DirFromPath "C:\opt\ff-extra;C:\bin" "C:\opt\ff"
  Assert-Equal "substring entry preserved" "C:\opt\ff-extra;C:\bin" $r.Path
  Assert-Equal "substring is not a removal" 0 $r.Removed

  $r = Remove-DirFromPath "C:\x;C:\x" "C:\x"
  Assert-Equal "duplicate exact entries all removed" "" $r.Path
  Assert-Equal "duplicates all counted" 2 $r.Removed

  $r = Remove-DirFromPath "C:\a;C:\b;C:\c" "C:\b"
  Assert-Equal "order of survivors preserved" "C:\a;C:\c" $r.Path
  Assert-Equal "middle removal counted" 1 $r.Removed

  $r = Remove-DirFromPath "" "C:\x"
  Assert-Equal "empty PATH stays empty" "" $r.Path
  Assert-Equal "empty PATH removes nothing" 0 $r.Removed

  $r = Remove-DirFromPath "C:\a" ""
  Assert-Equal "empty dir removes nothing" "C:\a" $r.Path
  Assert-Equal "empty dir counts nothing" 0 $r.Removed
}

# ---------- end-to-end through child runs (temp dirs only) ----------

function New-UJail {
  $dir = Join-Path ([System.IO.Path]::GetTempPath()) ("ff-ujail-" + [System.Guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Path (Join-Path $dir "bin") -Force | Out-Null
  return $dir
}

function Test-UninstallRemovesOnlyOwnedFiles {
  $dir = New-UJail
  try {
    $bin = Join-Path $dir "bin"
    [System.IO.File]::WriteAllText((Join-Path $bin "ff.exe"), "not a real binary")
    [System.IO.File]::WriteAllText((Join-Path $bin "other.txt"), "decoy")
    $sub = Join-Path $bin "sub"
    New-Item -ItemType Directory -Path $sub -Force | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $sub "nested.txt"), "decoy")

    $exe = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
    $r = Invoke-Child $exe @('-NoProfile', '-File', $uninstallerPath, '-InstallDir', $bin)
    Assert-Equal "uninstall exits 0" 0 $r.Code
    Assert-True "uninstall reports removal" ($r.Output -match "Binary removed")
    Assert-True "binary is gone" (-not (Test-Path (Join-Path $bin "ff.exe")))
    Assert-True "sibling file preserved" (Test-Path (Join-Path $bin "other.txt"))
    Assert-True "subdirectory preserved" (Test-Path (Join-Path $sub "nested.txt"))
    Assert-True "unknown version reported honestly" ($r.Output -match "unknown")
    Assert-True "piped output has no ANSI" ($r.Output -notmatch [char]27)
  } finally {
    Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
  }
}

function Test-UninstallAbsentIsIdempotent {
  $dir = New-UJail
  try {
    $bin = Join-Path $dir "bin"
    $exe = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
    $r = Invoke-Child $exe @('-NoProfile', '-File', $uninstallerPath, '-InstallDir', $bin)
    Assert-Equal "absent uninstall exits 0" 0 $r.Code
    Assert-True "absent uninstall says so" ($r.Output -match "not installed")
    $r = Invoke-Child $exe @('-NoProfile', '-File', $uninstallerPath, '-InstallDir', $bin)
    Assert-Equal "second uninstall still exits 0" 0 $r.Code
  } finally {
    Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
  }
}

function Test-RemovePathAbsentChangesNothing {
  # -RemovePath with no matching entry must not write User PATH at all:
  # the report says so and the run stays read-only.
  $dir = New-UJail
  try {
    $bin = Join-Path $dir "bin"
    [System.IO.File]::WriteAllText((Join-Path $bin "ff.exe"), "not a real binary")
    $before = [Environment]::GetEnvironmentVariable("Path", "User")
    $exe = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
    $r = Invoke-Child $exe @('-NoProfile', '-File', $uninstallerPath, '-InstallDir', $bin, '-RemovePath')
    $after = [Environment]::GetEnvironmentVariable("Path", "User")
    Assert-Equal "absent entry exits 0" 0 $r.Code
    Assert-True "absent entry reported" ($r.Output -match "did not contain")
    Assert-Equal "User PATH untouched" "$before" "$after"
  } finally {
    Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
  }
}

function Test-UninstallRefusesForcefieldHome {
  # Points at the real ~/.forcefield subtree (read-only: the refusal fires
  # before any filesystem write, and the target binary does not exist).
  $homeDir = $HOME
  if (-not $homeDir) { $homeDir = $env:USERPROFILE }
  $ffhome = Join-Path $homeDir ".forcefield"
  $exe = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
  $r = Invoke-Child $exe @('-NoProfile', '-File', $uninstallerPath, '-InstallDir', (Join-Path $ffhome "bin"))
  Assert-Equal "refusal exits nonzero" 1 $r.Code
  Assert-True "refusal explains itself" ($r.Output -match "Refusing to uninstall")
}

# ---------- run ----------

Test-LibOnlyImportIsSilent
Test-RemoveDirFromPath
Test-UninstallRemovesOnlyOwnedFiles
Test-UninstallAbsentIsIdempotent
Test-RemovePathAbsentChangesNothing
Test-UninstallRefusesForcefieldHome

Write-Host ""
Write-Host "$script:pass passed, $script:fail failed"
if ($script:fail -ne 0) { exit 1 }
