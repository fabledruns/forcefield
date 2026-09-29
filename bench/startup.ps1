# FF STARTUP BENCHMARK (Windows PowerShell)
#
# Measures Forcefield process startup latency only: process start until
# local exit. No model inference, no provider traffic, no MCP servers.
#
# Workload: `ff run --agent __bench_bogus__ "hi"` exercises the full
# startup path (config load, runtime construction incl. agents/skills/
# permissions/MCP host build, agent validation) then exits locally at
# agent validation - the same boundary as bench/README.md ("agent
# validation"). Exit code is nonzero by design; only wall time matters.
#
# Cold: fresh isolated HOME + workdir per replication.
# Warm: shared isolated HOME + workdir, first 5 runs discarded.
#
# Reports n/min/p50/p95/p99/max per scenario. Compare scenarios against
# each other on the same machine; absolute numbers vary by machine.
param(
  [int]$ColdN = 15,
  [int]$WarmN = 15,
  [int]$WarmDiscard = 5
)

$ErrorActionPreference = 'Stop'
$root = Join-Path ([IO.Path]::GetTempPath()) 'ff-startup-bench'
$ff = Join-Path $root 'ff.exe'

Write-Host 'building ff.exe from current source...'
& go build -o $ff (Join-Path $PSScriptRoot '..')
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }

function Measure-Run($ffhome, $work) {
  $env:USERPROFILE = $ffhome
  Push-Location $work
  try {
    # The workload exits nonzero by design (unknown agent after full
    # startup); keep native stderr text from becoming a terminating error.
    $prevAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $sw = [Diagnostics.Stopwatch]::StartNew()
    # First cold runs print a "created default config" notice on stderr;
    # discard all child output: only wall time matters here.
    & $ff run --agent __bench_bogus__ 'hi' >$null 2>$null
    $sw.Stop()
    $ErrorActionPreference = $prevAction
    return $sw.Elapsed.TotalMilliseconds
  } finally {
    Pop-Location
  }
}

function Stats($samples) {
  $s = @($samples | Sort-Object)
  $n = $s.Count
  $pct = { param($p) $s[[int][Math]::Ceiling($p / 100 * $n) - 1] }
  return [ordered]@{
    n   = $n
    min = [Math]::Round($s[0], 1)
    p50 = [Math]::Round((& $pct 50), 1)
    p95 = [Math]::Round((& $pct 95), 1)
    p99 = [Math]::Round((& $pct 99), 1)
    max = [Math]::Round($s[$n - 1], 1)
  }
}

# Cold: fresh dirs every replication.
$cold = @()
for ($i = 0; $i -lt $ColdN; $i++) {
  $h = Join-Path $root "cold-home-$i"
  $w = Join-Path $root "cold-work-$i"
  New-Item -ItemType Directory -Force $h, $w | Out-Null
  $cold += Measure-Run $h $w
}

# Warm: shared dirs, discard first runs.
$wh = Join-Path $root 'warm-home'
$ww = Join-Path $root 'warm-work'
New-Item -ItemType Directory -Force $wh, $ww | Out-Null
for ($i = 0; $i -lt $WarmDiscard; $i++) { Measure-Run $wh $ww | Out-Null }
$warm = @()
for ($i = 0; $i -lt $WarmN; $i++) { $warm += Measure-Run $wh $ww }

$cs = Stats $cold
$ws = Stats $warm
Write-Host ''
Write-Host 'FF STARTUP BENCHMARK'
Write-Host ''
Write-Host 'Cold startup'
$cs.GetEnumerator() | ForEach-Object { Write-Host ('{0,-4}: {1}' -f $_.Key, $_.Value) }
Write-Host ''
Write-Host 'Warm startup'
$ws.GetEnumerator() | ForEach-Object { Write-Host ('{0,-4}: {1}' -f $_.Key, $_.Value) }
