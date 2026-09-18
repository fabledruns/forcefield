#Requires -Version 5.1
<#
.SYNOPSIS
  Behavior tests for scripts/install.ps1. No network, no side effects.
.DESCRIPTION
  Run from the repository root:
    pwsh -NoProfile -File scripts/tests/Test-Installer.ps1
    powershell -NoProfile -ExecutionPolicy Bypass -File scripts/tests/Test-Installer.ps1

  The installer is dot-sourced with $env:FORCEFIELD_LIB_ONLY = '1', which
  loads its functions without executing anything. Hand-rolled asserts, no
  framework (works on PowerShell 7 and Windows PowerShell 5.1).
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

$installerPath = Join-Path (Join-Path $PSScriptRoot "..") "install.ps1"
$uninstallerPath = Join-Path (Join-Path $PSScriptRoot "..") "uninstall.ps1"

# ---------- regression: importing must not execute ----------

function Import-Silent($path) {
  $exe = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
  $cmd = '$env:FORCEFIELD_LIB_ONLY=''1''; . ''' + $path + ''''
  $savedEap = $ErrorActionPreference
  $ErrorActionPreference = 'Continue'
  try {
    $raw = & $exe -NoProfile -Command $cmd 2>&1
    $code = $LASTEXITCODE
    $script:out = ($raw | Out-String)
  } finally {
    $ErrorActionPreference = $savedEap
  }
  return $code
}

function Test-LibOnlyImportIsSilent {
  $code = Import-Silent $installerPath
  Assert-Equal "lib-only install import exit code" 0 $code
  Assert-Equal "lib-only install import produces no output" "" $script:out.Trim()
}

function Test-LibOnlyUninstallImportIsSilent {
  $code = Import-Silent $uninstallerPath
  Assert-Equal "lib-only uninstall import exit code" 0 $code
  Assert-Equal "lib-only uninstall import produces no output" "" $script:out.Trim()
}

# ---------- load functions for the behavior tests below ----------

$env:FORCEFIELD_LIB_ONLY = '1'
. $installerPath
Remove-Item Env:\FORCEFIELD_LIB_ONLY -ErrorAction SilentlyContinue

function Test-LibOnlyImportExposesHelpers {
  Assert-True "Get-Arch is available" ((Get-Command Get-Arch -ErrorAction SilentlyContinue) -ne $null)
  Assert-True "Test-VersionFormat is available" ((Get-Command Test-VersionFormat -ErrorAction SilentlyContinue) -ne $null)
  Assert-True "Get-LatestVersion is available" ((Get-Command Get-LatestVersion -ErrorAction SilentlyContinue) -ne $null)
}

# ---------- version behavior ----------

function Test-NormalizeVersion {
  Assert-Equal "normalize strips v prefix" "1.2.3" (Get-NormalizedVersion "v1.2.3")
  Assert-Equal "normalize keeps bare version" "1.2.3" (Get-NormalizedVersion "1.2.3")
  Assert-Equal "normalize strips build metadata" "1.2.0-rc.1" (Get-NormalizedVersion "v1.2.0-rc.1+001")
  Assert-Equal "normalize empty stays empty" "" (Get-NormalizedVersion "")
}

function Test-CompareVersions {
  Assert-Equal "compare equal versions" 0 (Compare-Versions "v1.2.3" "1.2.3")
  Assert-Equal "compare older installed" -1 (Compare-Versions "v1.1.0" "v1.2.0")
  Assert-Equal "compare newer installed" 1 (Compare-Versions "v2.0.0" "v1.9.9")
  Assert-Equal "compare patch versions" -1 (Compare-Versions "1.2.3" "1.2.10")
  Assert-Equal "compare prerelease below release" -1 (Compare-Versions "v1.2.3-rc.1" "v1.2.3")
  Assert-Equal "compare missing parts as zero" 0 (Compare-Versions "v1.2" "1.2.0")
  Assert-Equal "compare uppercase V prefix" 0 (Compare-Versions "V1.2.3" "v1.2.3")
  Assert-Equal "compare build metadata ignored" 0 (Compare-Versions "v1.2.0+001" "1.2.0+002")
  Assert-Equal "compare rc ordering" -1 (Compare-Versions "v1.2.3-rc.1" "v1.2.3-rc.2")
  Assert-Equal "compare major beats minor" 1 (Compare-Versions "v2.0.0" "v1.99.99")
}

# Test-VersionFormat throws (via Fail) on malformed input, returns
# quietly on valid input.
function Test-VersionFormatValidation {
  Test-VersionFormat "v1.2.3"
  Assert-True "validate accepts v1.2.3" $true
  Test-VersionFormat "1.0.0-rc.1"
  Assert-True "validate accepts prerelease" $true
  Test-VersionFormat ""
  Assert-True "validate accepts empty (means latest)" $true
  Assert-Throws "validate rejects metachars" { Test-VersionFormat "v1.0; rm -rf /" }
  Assert-Throws "validate rejects traversal" { Test-VersionFormat "../evil" }
  Assert-Throws "validate rejects non-numeric start" { Test-VersionFormat "latest" }
}

function Assert-Throws($desc, [scriptblock]$block) {
  try {
    & $block | Out-Null
    $script:fail++
    Write-Host "FAIL - $desc (expected an error, got none)"
  } catch {
    $script:pass++
  }
}

function Test-InstalledVersion {
  $dir = Join-Path ([System.IO.Path]::GetTempPath()) ("ff-stubs-" + [System.Guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  try {
    $good = Join-Path $dir "ff-good.ps1"
    [System.IO.File]::WriteAllText($good, "Write-Output 'ff version v1.2.3'`r`n")
    Assert-Equal "installed parses ff --version" "v1.2.3" (Get-InstalledVersion $good)

    $old = Join-Path $dir "ff-old.ps1"
    [System.IO.File]::WriteAllText($old, "Write-Output 'ff version v1.0.0'`r`nexit 1`r`n")
    Assert-Equal "installed parses despite nonzero exit" "v1.0.0" (Get-InstalledVersion $old)

    $dev = Join-Path $dir "ff-dev.ps1"
    [System.IO.File]::WriteAllText($dev, "Write-Output 'ff version dev'`r`n")
    Assert-Equal "installed dev passes through" "dev" (Get-InstalledVersion $dev)

    $garbage = Join-Path $dir "ff-garbage.ps1"
    [System.IO.File]::WriteAllText($garbage, "Write-Output 'hello world'`r`n")
    Assert-Equal "installed garbage yields empty" "" (Get-InstalledVersion $garbage)

    Assert-Equal "installed missing binary yields empty" "" (Get-InstalledVersion (Join-Path $dir "nope.ps1"))
  } finally {
    Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
  }
}

function Test-InstallAction {
  Assert-Equal "decision fresh when absent" "fresh" (Get-InstallAction "" "v1.2.0" $false $false)
  Assert-Equal "decision upgrade when older" "upgrade" (Get-InstallAction "v1.1.0" "v1.2.0" $false $true)
  Assert-Equal "decision current when equal" "current" (Get-InstallAction "v1.2.0" "v1.2.0" $false $true)
  Assert-Equal "decision reinstall when equal plus force" "reinstall" (Get-InstallAction "v1.2.0" "v1.2.0" $true $true)
  Assert-Equal "decision downgrade refused" "refuse-downgrade" (Get-InstallAction "v2.0.0" "v1.2.0" $false $true)
  Assert-Equal "decision downgrade with force reinstalls" "reinstall" (Get-InstallAction "v2.0.0" "v1.2.0" $true $true)
  Assert-Equal "decision unknown version upgrades" "upgrade" (Get-InstallAction "dev" "v1.2.0" $false $true)
}

# ---------- installation path behavior ----------

function Test-InstallPaths {
  $homeDir = $HOME
  if (-not $homeDir) { $homeDir = $env:USERPROFILE }
  Assert-Equal "default dir is under home" (Join-Path $homeDir ".local\bin") (Get-DefaultInstallDir)
  Assert-Equal "normalize strips trailing slash" "C:\opt\ff" (Normalize-Dir "C:\opt\ff\")
  Assert-Equal "normalize keeps drive root" "C:\" (Normalize-Dir "C:\")
  Assert-Equal "normalize keeps clean path" "C:\opt\ff" (Normalize-Dir "C:\opt\ff")
  Assert-Equal "normalize empty stays empty" "" (Normalize-Dir "")
  Assert-True "dir found in PATH (case-insensitive)" (Test-DirInPath "C:\bin;C:\OPT\FF" "c:\opt\ff\")
  Assert-True "dir missing from PATH" (-not (Test-DirInPath "C:\bin" "C:\opt\ff"))
  Assert-True "dir substring is not a match" (-not (Test-DirInPath "C:\opt\ff-extra" "C:\opt\ff"))
  Assert-True "empty PATH has no match" (-not (Test-DirInPath "" "C:\opt\ff"))
}

# ---------- UI mode detection ----------
# Get-UiMode returns "<color|plain>+<unicode|ascii>". Each case pins the
# environment it needs and restores it afterwards.

function Use-UiEnv($vars, [scriptblock]$block) {
  $saved = @{}
  foreach ($k in $vars.Keys) {
    if (Test-Path "Env:\$k") { $saved[$k] = (Get-Item "Env:\$k").Value }
    if ($vars[$k] -eq $null) { Remove-Item "Env:\$k" -ErrorAction SilentlyContinue }
    else { Set-Item "Env:\$k" $vars[$k] }
  }
  try {
    return (& $block)
  } finally {
    foreach ($k in $vars.Keys) {
      if ($saved.ContainsKey($k)) { Set-Item "Env:\$k" $saved[$k] }
      else { Remove-Item "Env:\$k" -ErrorAction SilentlyContinue }
    }
  }
}

function Test-UiMode {
  Assert-Equal "piped output is plain" "plain+unicode" (Use-UiEnv @{
    LC_ALL = "C.UTF-8"; TERM = "xterm"; FORCEFIELD_TTY = "0"
    NO_COLOR = $null; FORCEFIELD_NO_COLOR = $null; FORCEFIELD_ASCII = $null
  } { Get-UiMode })
  Assert-Equal "NO_COLOR forces plain" "plain+unicode" (Use-UiEnv @{
    LC_ALL = "C.UTF-8"; TERM = "xterm"; FORCEFIELD_TTY = "1"; NO_COLOR = "1"
    FORCEFIELD_NO_COLOR = $null; FORCEFIELD_ASCII = $null
  } { Get-UiMode })
  Assert-Equal "dumb terminal is plain" "plain+unicode" (Use-UiEnv @{
    LC_ALL = "C.UTF-8"; TERM = "dumb"; FORCEFIELD_TTY = "1"
    NO_COLOR = $null; FORCEFIELD_NO_COLOR = $null; FORCEFIELD_ASCII = $null
  } { Get-UiMode })
  Assert-Equal "tty plus utf-8 is full color" "color+unicode" (Use-UiEnv @{
    LC_ALL = "C.UTF-8"; TERM = "xterm"; FORCEFIELD_TTY = "1"
    NO_COLOR = $null; FORCEFIELD_NO_COLOR = $null; FORCEFIELD_ASCII = $null
  } { Get-UiMode })
  Assert-Equal "ascii override wins" "color+ascii" (Use-UiEnv @{
    LC_ALL = "C.UTF-8"; TERM = "xterm"; FORCEFIELD_TTY = "1"; FORCEFIELD_ASCII = "1"
    NO_COLOR = $null; FORCEFIELD_NO_COLOR = $null
  } { Get-UiMode })
  Assert-Equal "non-utf8 locale is ascii" "color+ascii" (Use-UiEnv @{
    LC_ALL = "C"; TERM = "xterm"; FORCEFIELD_TTY = "1"
    NO_COLOR = $null; FORCEFIELD_NO_COLOR = $null; FORCEFIELD_ASCII = $null
    COLORTERM = $null
  } { Get-UiMode })
  Assert-Equal "missing TERM with COLORTERM still colors" "color+unicode" (Use-UiEnv @{
    LC_ALL = "C.UTF-8"; TERM = $null; COLORTERM = "truecolor"; FORCEFIELD_TTY = "1"
    NO_COLOR = $null; FORCEFIELD_NO_COLOR = $null; FORCEFIELD_ASCII = $null
  } { Get-UiMode })
  Assert-Equal "missing TERM and COLORTERM stays plain" "plain+unicode" (Use-UiEnv @{
    LC_ALL = "C.UTF-8"; TERM = $null; COLORTERM = $null; FORCEFIELD_TTY = "1"
    NO_COLOR = $null; FORCEFIELD_NO_COLOR = $null; FORCEFIELD_ASCII = $null
  } { Get-UiMode })
  Assert-Equal "non-utf8 console falls back to ascii" "color+ascii" (Use-NonUtf8Console {
    Use-UiEnv @{
      LC_ALL = "C.UTF-8"; TERM = "xterm"; FORCEFIELD_TTY = "1"
      NO_COLOR = $null; FORCEFIELD_NO_COLOR = $null; FORCEFIELD_ASCII = $null
    } { Get-UiMode }
  })
}

# Use-NonUtf8Console runs the block with a non-UTF-8 console output
# encoding (restored afterwards), simulating consoles that cannot render
# Unicode glyphs even when the locale claims UTF-8.
function Use-NonUtf8Console([scriptblock]$block) {
  $saved = $null
  try { $saved = [Console]::OutputEncoding } catch { $saved = $null }
  try {
    try { [Console]::OutputEncoding = [System.Text.Encoding]::GetEncoding(437) } catch {}
    return (& $block)
  } finally {
    if ($saved -ne $null) {
      try { [Console]::OutputEncoding = $saved } catch {}
    }
  }
}

# ---------- install-action seam (no network, stub binaries) ----------

function Test-ExistingInstallAction {
  $dir = Join-Path ([System.IO.Path]::GetTempPath()) ("ff-action-" + [System.Guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  try {
    Assert-Equal "action fresh when absent" "fresh" (Get-ExistingInstallAction "v1.2.0" $false (Join-Path $dir "ff.exe"))
    $older = Join-Path $dir "ff-older.ps1"
    [System.IO.File]::WriteAllText($older, "Write-Output 'ff version v1.1.0'`r`n")
    Assert-Equal "action upgrades older install" "upgrade" (Get-ExistingInstallAction "v1.2.0" $false $older)
    $same = Join-Path $dir "ff-same.ps1"
    [System.IO.File]::WriteAllText($same, "Write-Output 'ff version v1.2.0'`r`n")
    Assert-Equal "action current when equal" "current" (Get-ExistingInstallAction "v1.2.0" $false $same)
    Assert-Equal "action reinstalls equal with force" "reinstall" (Get-ExistingInstallAction "v1.2.0" $true $same)
    $newer = Join-Path $dir "ff-newer.ps1"
    [System.IO.File]::WriteAllText($newer, "Write-Output 'ff version v2.0.0'`r`n")
    Assert-Equal "action refuses downgrade" "refuse-downgrade" (Get-ExistingInstallAction "v1.2.0" $false $newer)
    Assert-Equal "action force overrides downgrade" "reinstall" (Get-ExistingInstallAction "v1.2.0" $true $newer)
  } finally {
    Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
  }
}

# ---------- gate end-to-end (needs the go toolchain, no network) ----------
# Builds a version-stamped ff.exe with the project's own ldflags pattern,
# then runs the real installer against it with pinned versions. The gate
# decides before any download, so these cases never touch the network.

function Test-InstallGateEndToEnd {
  $go = Get-Command go -ErrorAction SilentlyContinue
  if (-not $go) {
    Write-Host "SKIP gate e2e (go toolchain not available)"
    return
  }
  $repoRoot = Join-Path (Join-Path $PSScriptRoot "..") ".."
  $dir = Join-Path ([System.IO.Path]::GetTempPath()) ("ff-gate-" + [System.Guid]::NewGuid().ToString("N"))
  $instDir = Join-Path $dir "bin"
  New-Item -ItemType Directory -Path $instDir -Force | Out-Null
  try {
    $stub = Join-Path $dir "ff-stub.exe"
    Push-Location $repoRoot
    try {
      & go build -trimpath -ldflags "-X forcefield/cmd.Version=v9.9.9 -X main.Version=v9.9.9" -o $stub .
      $buildCode = $LASTEXITCODE
    } finally {
      Pop-Location
    }
    Assert-Equal "gate stub builds" 0 $buildCode
    $reported = (& $stub --version 2>&1 | Out-String).Trim()
    Assert-Equal "gate stub reports stamped version" "ff version v9.9.9" $reported
    Copy-Item $stub (Join-Path $instDir "ff.exe") -Force

    # Native stderr from the child must not terminate this script under
    # Windows PowerShell 5.1 ($ErrorActionPreference = 'Stop').
    $exe = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
    $savedEap = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
      $r = & $exe -NoProfile -File $installerPath -InstallDir $instDir -Version v1.0.0 -NoModifyPath 2>&1
      $c = $LASTEXITCODE
      $text = ($r | Out-String)
    } finally {
      $ErrorActionPreference = $savedEap
    }
    Assert-Equal "gate exits nonzero on downgrade" 1 $c
    Assert-True "gate refuses downgrade" ($text -match "refusing to downgrade")

    $ErrorActionPreference = 'Continue'
    try {
      $r = & $exe -NoProfile -File $installerPath -InstallDir $instDir -Version v9.9.9 -NoModifyPath 2>&1
      $c = $LASTEXITCODE
      $text = ($r | Out-String)
    } finally {
      $ErrorActionPreference = $savedEap
    }
    Assert-Equal "gate exits 0 when current" 0 $c
    Assert-True "gate reports already-installed" ($text -match "already installed")
    $leftovers = @(Get-ChildItem $instDir | Select-Object -ExpandProperty Name | Sort-Object)
    Assert-Equal "gate leaves install dir untouched" "ff.exe" ($leftovers -join ",")
  } finally {
    Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
  }
}

# ---------- UI rendering invariants ----------
# Format-* builders are pure (explicit -UiMode): pin the mode, assert
# small invariants, never full-screen snapshots.

function Test-UiRendering {
  # Glyph expectations use [char] codes so this file stays pure ASCII:
  # WinPS 5.1 misreads BOM-less UTF-8 bytes as smart quotes and fails to
  # parse. U+2713 done, U+203A active, U+25CB pending, U+2715 failure.
  $esc = [char]27
  $ug = Get-UiGlyphs "color+unicode"
  Assert-Equal "unicode done glyph" ([char]0x2713) $ug.Ok
  Assert-Equal "unicode active glyph" ([char]0x203A) $ug.Step
  Assert-Equal "unicode pending glyph" ([char]0x25CB) $ug.Pend
  Assert-Equal "unicode failure glyph" ([char]0x2715) $ug.Fail
  Assert-Equal "unicode warning glyph" "!" $ug.Warn
  $ag = Get-UiGlyphs "color+ascii"
  Assert-Equal "ascii done glyph" "*" $ag.Ok
  Assert-Equal "ascii active glyph" ">" $ag.Step
  Assert-Equal "ascii pending glyph" "o" $ag.Pend
  Assert-Equal "ascii failure glyph" "x" $ag.Fail
  Assert-Equal "ascii warning glyph" "!" $ag.Warn

  $plain = Format-Brand "plain+ascii"
  Assert-True "plain brand has no ANSI" ($plain -notmatch $esc)
  Assert-True "brand names FORCE" ($plain -match "FORCE")
  Assert-True "brand names FIELD" ($plain -match "FIELD")
  Assert-True "brand carries tagline" ($plain -match "local-first agent harness")

  Assert-Equal "row aligns label and value" "  AB           c" (Format-Row "AB" "c")
  Assert-Equal "plain ascii step" "> Doing" (Format-Step "plain+ascii" "Doing")
  Assert-Equal "plain ascii done" "* Did" (Format-Done "plain+ascii" "Did")
  Assert-True "fail marks glyph" ((Format-Fail "plain+unicode" "boom") -match ([char]0x2715))
  Assert-True "plain output has no ANSI" ((Format-Step "plain+ascii" "x") -notmatch $esc)
}

# ---------- source hygiene ----------
# PowerShell scripts must stay pure ASCII: WinPS 5.1 misreads BOM-less
# UTF-8 bytes as smart quotes and fails to parse (glyphs come from
# [char] codes instead). This pins the invariant on every run.

function Test-ScriptsStayAscii {
  foreach ($f in @($installerPath, $uninstallerPath, $PSCommandPath)) {
    $bytes = [System.IO.File]::ReadAllBytes($f)
    $bad = @($bytes | Where-Object { $_ -gt 127 })
    Assert-Equal ("no non-ASCII bytes in " + (Split-Path $f -Leaf)) 0 $bad.Count
  }
}

# ---------- run ----------

Test-LibOnlyImportIsSilent
Test-LibOnlyUninstallImportIsSilent
Test-LibOnlyImportExposesHelpers
Test-NormalizeVersion
Test-CompareVersions
Test-VersionFormatValidation
Test-InstalledVersion
Test-InstallAction
Test-InstallPaths
Test-UiMode
Test-ExistingInstallAction
Test-InstallGateEndToEnd
Test-UiRendering
Test-ScriptsStayAscii

Write-Host ""
Write-Host "$script:pass passed, $script:fail failed"
if ($script:fail -ne 0) { exit 1 }
