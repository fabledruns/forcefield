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

function Write-Info($msg) { Write-Host "[INFO] $msg" }
function Write-Warn($msg) { Write-Host "[WARN] $msg" -ForegroundColor Yellow }
function Fail($msg) {
  Write-Host "[ERROR] $msg" -ForegroundColor Red
  throw $msg
}

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

$Arch = Get-Arch
Write-Info "Detected: windows/$Arch"

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
  Write-Info "Resolving latest release from GitHub..."
  $Version = Get-LatestVersion
  if (-not $Version) {
    Fail "Could not determine latest release. GitHub API may be rate-limited or offline. Try: install.ps1 -Version v1.0.0 or set `$env:FORCEFIELD_VERSION='v1.0.0'. Manual: https://github.com/$Repo/releases"
  }
  Write-Info "Latest release: $Version"
} else {
  if ($Version -notmatch '^[vV]') { $Version = "v$Version" }
  Write-Info "Requested version: $Version"
}
Test-VersionFormat $Version

$Artifact = "ff-windows-$Arch.exe"
$DownloadUrl = "https://github.com/$Repo/releases/download/$Version/$Artifact"
$ChecksumUrl = "https://github.com/$Repo/releases/download/$Version/checksums.txt"

Write-Info "Artifact: $Artifact"
Write-Info "URL: $DownloadUrl"

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
  # Download artifact
  $dest = Join-Path $tmp $Artifact
  Write-Info "Downloading $DownloadUrl ..."
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

  # Verify checksum
  Write-Info "Verifying checksum..."
  $checksumFile = Join-Path $tmp "checksums.txt"
  $checksumOk = $false
  $foundChecksumUrl = $null
  foreach ($url in @($ChecksumUrl, "https://github.com/$Repo/releases/download/$Version/SHA256SUMS", "https://github.com/$Repo/releases/download/$Version/checksums.sha256")) {
    try {
      Invoke-WebRequest -Uri $url -OutFile $checksumFile -UseBasicParsing -Headers @{"User-Agent"="forcefield-installer"} -ErrorAction Stop
      if ((Test-Path $checksumFile) -and (Get-Item $checksumFile).Length -gt 0) {
        $foundChecksumUrl = $url
        Write-Info "Found checksums at $url"
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
      Write-Warn "checksums.txt does not contain $Artifact; skipping verification (this should not happen for new releases)"
    } else {
      $actualHash = (Get-FileHash -Path $dest -Algorithm SHA256).Hash.ToLower()
      if ($expected -ne $actualHash) {
        Fail "Checksum mismatch for $Artifact`n  expected: $expected`n  actual:   $actualHash`nRefusing to install. The download may be corrupted or tampered with."
      }
      Write-Info "Checksum OK: $actualHash"
      $checksumOk = $true
    }
  } else {
    Write-Warn "No checksums file at $ChecksumUrl"
    Write-Warn "Skipping verification (old releases before v1.2 may not have checksums)"
    Write-Warn "For security, prefer a release with checksums.txt or verify manually"
  }

  # Install
  Write-Info "Installing to $InstallDir ..."
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

  $target = Join-Path $InstallDir $BinaryName
  if (Test-Path $target) {
    Write-Info "Existing installation found at $target (upgrading in place)"
  } else {
    Write-Info "No existing installation at $target"
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
    throw
  }
  Write-Info "Installed $target"

  # Verify
  $installedVer = $null
  try {
    $out = & $target --version 2>&1 | Out-String
    $installedVer = $out.Trim().Split("`n")[0].Trim()
    if ($installedVer) { Write-Info "Verified: $installedVer" }
    # Reset exit code: external --version on older binaries returns 1, but installer should not fail
    $global:LASTEXITCODE = 0
  } catch {
    Write-Warn "Installed binary does not support --version; checking --help instead"
    try { & $target --help 2>&1 | Out-Null; $global:LASTEXITCODE = 0 } catch {
      Write-Warn "Installed binary failed to run --help; it may be the wrong architecture"
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

  if ($alreadyInPath) {
    Write-Info "PATH already contains $InstallDir (user PATH)"
  } else {
    $needsPathUpdate = $true
    if ($NoModifyPath) {
      Write-Warn "PATH does not contain $InstallDir (path modification disabled via -NoModifyPath)"
    } else {
      Write-Info "PATH does not contain $InstallDir; adding to user PATH..."
      if ($userPath -and $userPath.Trim() -ne "") {
        $newPath = "$userPath;$InstallDir"
      } else {
        $newPath = $InstallDir
      }
      # Avoid duplicate: ensure not already present (case-insensitive)
      [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
      Write-Info "Updated user PATH. Added $InstallDir"
      # Also update current session
      if (-not $procAlreadyInPath) {
        $env:Path = "$env:Path;$InstallDir"
      }
      Write-Info "Updated current session PATH for this terminal."
    }
  }

  # Final output
  Write-Host ""
  Write-Host "Forcefield installed successfully!" -ForegroundColor Green
  Write-Host "  Binary:     $target"
  if ($installedVer) { Write-Host "  Version:    $installedVer" } else { Write-Host "  Version:    $Version (run ff --version)" }
  Write-Host "  Install dir: $InstallDir"

  if ($needsPathUpdate) {
    Write-Host ""
    Write-Host "PATH update needed:" -ForegroundColor Yellow
    Write-Host "  $InstallDir is not in your current PATH for new terminals."
    if (-not $NoModifyPath) {
      Write-Host "  The installer added it to your user PATH."
      Write-Host "  Restart your terminal (or VS Code) or run:"
      Write-Host "    `$env:Path += `";$InstallDir`""
    } else {
      Write-Host "  Add it manually (user PATH, not system):"
      Write-Host "    [Environment]::SetEnvironmentVariable('Path', `"`$env:Path;$InstallDir`", 'User')"
    }
    Write-Host ""
    Write-Host "  After updating PATH, verify with:"
    Write-Host "    ff --version"
    Write-Host "    ff doctor"
  } else {
    Write-Host "  Run: ff --version"
    Write-Host "       ff doctor"
  }

  if (-not $checksumOk) {
    Write-Host ""
    Write-Host "Note: checksum verification was skipped or unavailable. For production, use a release with checksums.txt." -ForegroundColor Yellow
  }

  Write-Host ""
  Write-Host "Documentation: https://github.com/$Repo"
  Write-Host "Releases:      https://github.com/$Repo/releases"
  Write-Host ""
  $global:LASTEXITCODE = 0
} finally {
  & $cleanup
  try { if ($tmpTarget -and (Test-Path $tmpTarget)) { Remove-Item $tmpTarget -Force -ErrorAction SilentlyContinue } } catch {}
  # Ensure the installer itself exits 0 on success, even if subprocess set LASTEXITCODE
  if ($global:LASTEXITCODE -ne 0 -and $Error.Count -eq 0) { $global:LASTEXITCODE = 0 }
}
