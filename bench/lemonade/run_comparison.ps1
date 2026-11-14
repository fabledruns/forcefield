<#
.SYNOPSIS
Reproducible 3-way Forcefield x AMD Lemonade comparison benchmark for Windows 11.

.DESCRIPTION
Compares Forcefield Babbage across:
  1. Existing provider baseline (default: openai-compatible)
  2. Lemonade with disable_slot_guard: true
  3. Lemonade with SlotGuard enabled (disable_slot_guard: false)

Requires a live local Lemonade server. Never fabricates benchmark numbers.
#>
[CmdletBinding()]
param(
    [string]$LemonadeUrl = $(if ($env:LEMONADE_URL) { $env:LEMONADE_URL } else { "http://localhost:13305/api/v1" }),
    [string]$BaselineType = $(if ($env:BASELINE_TYPE) { $env:BASELINE_TYPE } else { "openai-compatible" }),
    [string]$BaselineUrl = $(if ($env:BASELINE_URL) { $env:BASELINE_URL } else { "" }),
    [string]$Model = $(if ($env:MODEL) { $env:MODEL } else { "" }),
    [string]$Tasks = $(if ($env:TASKS) { $env:TASKS } else { "DELEG-001,DELEG-002" }),
    [string]$Timeout = $(if ($env:BENCH_TIMEOUT) { $env:BENCH_TIMEOUT } else { "180s" }),
    [string]$Binary = $(if ($env:FF_BIN) { $env:FF_BIN } else { "" }),
    [string]$OutDir = ""
)

$ErrorActionPreference = "Stop"

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$BaseOrigin = ($LemonadeUrl.TrimEnd('/') -replace '/api/v1$', '' -replace '/v1$', '')
if (-not $BaselineUrl) {
    $BaselineUrl = $LemonadeUrl
}

Write-Host "Checking live Lemonade server at $BaseOrigin..."
try {
    $live = Invoke-RestMethod -Uri "$BaseOrigin/live" -TimeoutSec 3
} catch {
    try {
        $live = Invoke-RestMethod -Uri "$BaseOrigin/api/v1/live" -TimeoutSec 3
    } catch {
        Write-Error "Lemonade server not reachable at $BaseOrigin/live. Start lemonade-server before running live benchmarks. Error: $_"
        exit 1
    }
}

$health = $null
try {
    $health = Invoke-RestMethod -Uri "$BaseOrigin/api/v1/health" -TimeoutSec 5
} catch {
    Write-Error "Lemonade /api/v1/health failed at $BaseOrigin: $_"
    exit 1
}

if (-not $Model) {
    if ($health.model_loaded) {
        $Model = [string]$health.model_loaded
    } else {
        Write-Error "No model is currently loaded on Lemonade (/api/v1/health reported model_loaded empty) and -Model was not specified."
        exit 1
    }
}

$Timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
if (-not $OutDir) {
    $OutDir = Join-Path $PSScriptRoot "results\$Timestamp"
}
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

if (-not $Binary) {
    $Binary = Join-Path $OutDir "ff.exe"
    Write-Host "Building ff.exe into $Binary..."
    Push-Location $RepoRoot
    try {
        & go build -o $Binary .
        if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
    } finally {
        Pop-Location
    }
}

$TaskList = @()
if ($Tasks -ne "all") {
    $TaskList = $Tasks.Split(',') | ForEach-Object { $_.Trim() } | Where-Object { $_ }
}

function Invoke-BenchMode {
    param(
        [string]$ModeName,
        [string]$ProviderType,
        [string]$ProviderUrl,
        [bool]$DisableSlotGuard
    )

    $ModeHome = Join-Path $OutDir "home-$ModeName"
    $CfgDir = Join-Path $ModeHome ".forcefield"
    $ResDir = Join-Path $OutDir "raw-$ModeName"
    New-Item -ItemType Directory -Force -Path $CfgDir, $ResDir | Out-Null

    $disableLine = if ($ProviderType -eq "lemonade") { "    disable_slot_guard: $($DisableSlotGuard.ToString().ToLowerInvariant())`n" } else { "" }
    $cfgYaml = @"
model:
  provider: target
  name: $Model
providers:
  target:
    type: $ProviderType
    base_url: $ProviderUrl
$disableLine"@
    Set-Content -Path (Join-Path $CfgDir "config.yaml") -Value $cfgYaml -Encoding UTF8

    $prevHome = $env:HOME
    $prevUserProfile = $env:USERPROFILE
    $prevXdg = $env:XDG_CONFIG_HOME
    try {
        $env:HOME = $ModeHome
        $env:USERPROFILE = $ModeHome
        $env:XDG_CONFIG_HOME = $ModeHome

        if ($TaskList.Count -eq 0) {
            Write-Host "[$ModeName] Running full benchmark suite..."
            $outPath = Join-Path $OutDir "$ModeName.json"
            & $Binary bench run --results $ResDir --timeout $Timeout --json | Out-Null
            $latest = Get-ChildItem -Path $ResDir -Filter "*.json" | Sort-Object LastWriteTime -Descending | Select-Object -First 1
            if ($latest) { Copy-Item $latest.FullName $outPath -Force }
        } else {
            Write-Host "[$ModeName] Running tasks: $($TaskList -join ', ')..."
            foreach ($t in $TaskList) {
                $tResDir = Join-Path $ResDir $t
                New-Item -ItemType Directory -Force -Path $tResDir | Out-Null
                & $Binary bench run --task $t --results $tResDir --timeout $Timeout | Out-Null
            }
            $jsonFiles = Get-ChildItem -Path $ResDir -Recurse -Filter "*.json"
            $allData = @()
            foreach ($jf in $jsonFiles) {
                $allData += (Get-Content $jf.FullName -Raw | ConvertFrom-Json)
            }
            $allData | ConvertTo-Json -Depth 12 | Set-Content (Join-Path $OutDir "$ModeName-tasks.json") -Encoding UTF8
        }
    } finally {
        $env:HOME = $prevHome
        $env:USERPROFILE = $prevUserProfile
        $env:XDG_CONFIG_HOME = $prevXdg
    }
}

Invoke-BenchMode -ModeName "baseline" -ProviderType $BaselineType -ProviderUrl $BaselineUrl -DisableSlotGuard $false
Invoke-BenchMode -ModeName "lemonade-unguarded" -ProviderType "lemonade" -ProviderUrl $LemonadeUrl -DisableSlotGuard $true
Invoke-BenchMode -ModeName "lemonade-slotguard" -ProviderType "lemonade" -ProviderUrl $LemonadeUrl -DisableSlotGuard $false

$SummaryPath = Join-Path $OutDir "comparison.md"
$lines = @(
    "# Forcefield x Lemonade Live Comparison",
    "",
    "- Timestamp: $Timestamp",
    "- Endpoint: ``$LemonadeUrl``",
    "- Model: ``$Model``",
    "- Tasks: ``$Tasks``",
    "- Raw per-mode JSON directories live under ``$OutDir/raw-*``.",
    "",
    "| Mode | Tasks | Passed | Total (ms) | p50 (ms) | p95 (ms) | Avg Gen tok/s | Cached Tokens | SlotGuard Waits | Slot Wait (ms) |",
    "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"
)

foreach ($mode in @("baseline", "lemonade-unguarded", "lemonade-slotguard")) {
    $rawDir = Join-Path $OutDir "raw-$mode"
    $reports = Get-ChildItem -Path $rawDir -Recurse -Filter "*.json"
    $totalTasks = 0; $passed = 0; $totalDur = 0; $p50s = @(); $p95s = @(); $tpsList = @(); $cached = 0; $waits = 0; $waitMS = 0
    foreach ($rf in $reports) {
        $r = Get-Content $rf.FullName -Raw | ConvertFrom-Json
        if ($r.summary) {
            $totalTasks += [int]$r.summary.total_tasks
            $passed += [int]$r.summary.passed_tasks
            $totalDur += [int64]$r.summary.total_duration_ms
            if ($r.summary.latency_p50_ms) { $p50s += [double]$r.summary.latency_p50_ms }
            if ($r.summary.latency_p95_ms) { $p95s += [double]$r.summary.latency_p95_ms }
            if ($r.summary.avg_predicted_per_second) { $tpsList += [double]$r.summary.avg_predicted_per_second }
            if ($r.summary.tokens -and $r.summary.tokens.cached_tokens) { $cached += [int]$r.summary.tokens.cached_tokens }
            $waits += [int]$r.summary.slot_guard_waits
            $waitMS += [int64]$r.summary.slot_guard_wait_ms
        }
    }
    $avgP50 = if ($p50s.Count -gt 0) { [math]::Round(($p50s | Measure-Object -Average).Average, 0) } else { 0 }
    $avgP95 = if ($p95s.Count -gt 0) { [math]::Round(($p95s | Measure-Object -Average).Average, 0) } else { 0 }
    $avgTps = if ($tpsList.Count -gt 0) { [math]::Round(($tpsList | Measure-Object -Average).Average, 2) } else { 0 }
    $lines += "| $mode | $totalTasks | $passed | $totalDur | $avgP50 | $avgP95 | $avgTps | $cached | $waits | $waitMS |"
}

$lines | Set-Content -Path $SummaryPath -Encoding UTF8
Write-Host ""
Get-Content $SummaryPath
Write-Host ""
Write-Host "Comparison saved to: $SummaryPath"
