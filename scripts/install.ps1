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

  The installer never requires Administrator privileges, never touches
  system PATH, and never removes ~/.forcefield.

.PARAMETER Version
  Release tag to install, e.g. v1.0.0. Defaults to latest release.

.PARAMETER InstallDir
  Directory to install ff.exe into. Defaults to $HOME\.local\bin

.PARAMETER NoModifyPath
  Do not modify the user PATH.

.PARAMETER Help
  Show help.
#>
[CmdletBinding()]
param(
  [string]$Version = "",
  [string]$InstallDir = "",
  [switch]$NoModifyPath,
  [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$Repo = "fabledruns/forcefield"
$BinaryName = "ff.exe"

# ─────────────────────────────────────────────────────────────────────────────
# Terminal UI (presentation only)
# Installer behavior, errors, paths, downloads and checks remain unchanged.
# Normal output stays compact; full diagnostics go to -Verbose.
# Symbols: ◈ section · ◇ active · ✦ done · ✳ warning · × failure
# ─────────────────────────────────────────────────────────────────────────────

# Keep geometric symbols intact on modern Windows terminals (best effort only).
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}

$script:UiQuiet = $false

function Write-Brand {
  Write-Host ""
  Write-Host "forcefield" -ForegroundColor Red
}

function Write-Section($name) {
  Write-Host ""
  Write-Host "◈ $name"
}

function Write-Active($msg) {
  Write-Host "◇ $msg"
}

function Write-Done($msg) {
  Write-Host "✦ $msg" -ForegroundColor Green
}

function Write-Row($label, $value) {
  $padded = "$label".PadRight(12)
  Write-Host "✦ $padded$value" -ForegroundColor Green
}

function Write-WarnUi($msg) {
  Write-Host "✳ $msg" -ForegroundColor Yellow
}

function Write-Detail($msg) {
  Write-Host "  $msg" -ForegroundColor DarkGray
}

function Write-Info($msg) {
  Write-Host "  $msg" -ForegroundColor DarkGray
}

function Write-Warn($msg) {
  Write-Host "✳ $msg" -ForegroundColor Yellow
}

function Write-Step($label) {
  Write-Host "◇ $label"
}

function Write-Ok($label) {
  Write-Host "✦ $label" -ForegroundColor Green
}

function Write-Dim($msg) {
  Write-Host "  $msg" -ForegroundColor DarkGray
}

function Fail($msg) {
  Write-Host ""
  $text = "$msg"
  $lines = $text -split "`n"
  Write-Host "× $($lines[0])" -ForegroundColor Red
  for ($i = 1; $i -lt $lines.Count; $i++) {
    Write-Host "  $($lines[$i])"
  }
  throw $msg
}

function Write-Rule {
  Write-Host "────────────────────────────────────────────────────────────" -ForegroundColor DarkGray
}

Write-Brand

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
Test-VersionFormat $Version

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

Write-Section "environment"
$Arch = Get-Arch
Write-Row "platform" "windows/$Arch"
Write-Verbose "platform: windows/$Arch"

# Resolve latest version if not specified
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

if (-not $Version -or $Version.Trim() -eq "") {
  Write-Active "resolving release"
  Write-Verbose "resolving latest release via GitHub API"
  $Version = Get-LatestVersion
  if (-not $Version) {
    Fail "Could not determine latest release. GitHub API may be rate-limited or offline. Try: install.ps1 -Version v1.0.0 or set `$env:FORCEFIELD_VERSION='v1.0.0'. Manual: https://github.com/$Repo/releases"
  }
  Write-Row "release" "$Version"
} else {
  if ($Version -notmatch '^[vV]') { $Version = "v$Version" }
  Write-Row "release" "$Version"
}
Test-VersionFormat $Version
Write-Verbose "release: $Version"

$Artifact = "ff-windows-$Arch.exe"
$DownloadUrl = "https://github.com/$Repo/releases/download/$Version/$Artifact"
$ChecksumUrl = "https://github.com/$Repo/releases/download/$Version/checksums.txt"

Write-Row "artifact" "$Artifact"
Write-Verbose "artifact: $Artifact"
Write-Verbose "download: $DownloadUrl"
Write-Verbose "checksums: $ChecksumUrl"

# Create temp dir
$tmpRoot = [System.IO.Path]::GetTempPath()
$tmp = Join-Path $tmpRoot "forcefield-install-$(Get-Random)-$PID"
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
# Ensure cleanup on exit
$cleanup = {
  try { if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue } } catch {}
}
# Use try/finally for main logic; also register engine exit
try {
  Write-Section "installation"
  # Download artifact
  $dest = Join-Path $tmp $Artifact
  Write-Active "fetching release"
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
  Write-Done "release fetched"

  # Verify checksum
  Write-Active "verifying integrity"
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
      Write-WarnUi "checksum verification unavailable"
      Write-Detail "Continuing without verification."
      Write-Detail "Prefer a release with checksums.txt."
      Write-Verbose "checksums.txt does not contain $Artifact; skipping verification"
    } else {
      $actualHash = (Get-FileHash -Path $dest -Algorithm SHA256).Hash.ToLower()
      if ($expected -ne $actualHash) {
        Fail "Checksum mismatch for $Artifact`n  expected: $expected`n  actual:   $actualHash`nRefusing to install. The download may be corrupted or tampered with."
      }
      Write-Done "checksum verified"
      $checksumOk = $true
    }
  } else {
    Write-WarnUi "checksum verification unavailable"
    Write-Detail "Continuing without verification."
    Write-Detail "Prefer a release with checksums.txt."
    Write-Verbose "No checksums file at $ChecksumUrl (old releases may not have checksums)"
  }

  # Install
  Write-Active "installing runtime"
  Write-Verbose "installing to $InstallDir"
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

  $target = Join-Path $InstallDir $BinaryName
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
    Write-Host ""
    Write-Host "× failed to install runtime" -ForegroundColor Red
    Write-Host "  $_"
    throw
  }
  Write-Verbose "installed $target"
  if ($isUpgrade) {
    Write-Done "runtime upgraded"
  } else {
    Write-Done "runtime installed"
  }

  # Verify
  $installedVer = $null
  try {
    $out = & $target --version 2>&1 | Out-String
    $installedVer = $out.Trim().Split("`n")[0].Trim()
    if ($installedVer) { Write-Verbose "binary: $installedVer" }
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
    Write-Done "PATH ready"
  } else {
    Write-Active "configuring PATH"
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
  Write-Host ""
  Write-Host "✦ Forcefield $Version" -ForegroundColor Green
  Write-Host ""
  Write-Host "ready."
  Write-Host ""
  if ($needsPathUpdate) {
    if (-not $NoModifyPath) {
      Write-Host "Added to user PATH. Restart your terminal, or run:"
      Write-Host "  `$env:Path += `";$InstallDir`""
      Write-Host ""
    } else {
      Write-Host "Add it to your user PATH (not system):"
      Write-Host "  [Environment]::SetEnvironmentVariable('Path', `"`$env:Path;$InstallDir`", 'User')"
      Write-Host ""
    }
    Write-Detail "verify:"
    Write-Host "  ff --version"
    Write-Host "  ff doctor"
  } else {
    Write-Detail "try:"
    Write-Host "  ff --version"
    Write-Host "  ff doctor"
  }

  if (-not $checksumOk) {
    Write-Host ""
    Write-WarnUi "checksum verification unavailable"
    Write-Detail "Installed without verification. Prefer a release with checksums.txt."
  }

  Write-Host ""
  $global:LASTEXITCODE = 0
} finally {
  & $cleanup
  try { if ($tmpTarget -and (Test-Path $tmpTarget)) { Remove-Item $tmpTarget -Force -ErrorAction SilentlyContinue } } catch {}
  # Ensure the installer itself exits 0 on success, even if subprocess set LASTEXITCODE
  if ($global:LASTEXITCODE -ne 0 -and $Error.Count -eq 0) { $global:LASTEXITCODE = 0 }
}
