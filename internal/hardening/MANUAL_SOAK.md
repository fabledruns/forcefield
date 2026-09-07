# Manual multi-hour / multi-day soak (RC6 lab companion)

This is the non-CI counterpart to `soak_test.go`. The automated test models
the same growth mechanisms deterministically in seconds; this document
describes a real soak to validate RSS/FDs/processes over hours.

## 60-minute accelerated soak

```bash
go build -o ./bin/ff .
mkdir -p /tmp/ff-soak && cd /tmp/ff-soak
git init -q 2>/dev/null || true
export FF_SOAK_MODEL="ornith:9b"
../bin/ff run "inspect this repository, list files, and summarize structure" 2>&1 | tee soak-1.log
for i in $(seq 1 30); do
  ../bin/ff run "list files in . and report file count (iteration $i)" 2>&1 | tee -a soak-loop.log
done
ls -la .forcefield/sessions/ | tee sessions.txt
du -sh .forcefield/ | tee disk.txt
```

Monitor in a second terminal:

```bash
# RSS + FDs + goroutines (via ps/lsof; adjust for Windows with Task Manager / handle.exe)
ps -o pid,rss,vsz,etime -p $(pgrep -f 'ff run') 2>/dev/null || echo "use Task Manager on Windows"
ls .forcefield/sessions/*.json | wc -l
du -ch .forcefield/sessions/*.json | tail -1
```

## 8-hour / 5-day unattended checklist

- Run under `tmux`/`nohup` (Forcefield has no SIGHUP handling; terminal close loses the run).
- Use `workspace.mode: strict` (never `permissive` for unattended).
- Set `permissions.tools.read_file: ask` for dotfiles or keep default `ask` for shell/write.
- Monitor billing/quotas (429 quota is non-retryable by design; sustained 429 exhausts after 3 retries).
- Expect manual `ff --resume <id>` after crash/reboot (no auto-restart).
- Watch `.forcefield/sessions/*.json` size (cap 1000 messages; silent-drop compaction — check for `[compacted]` marker after RC6).
- Watch `.forcefield/traces/*.jsonl` count (4 MB per run cap; crash files kept — clean manually).
- On Windows: verify no leaked `bash`/tool children in Task Manager (grandchildren without job objects survive).

## Success criteria

- Session file stays parseable after kill -9 mid-run + resume.
- No unbounded RSS/FD growth across 30+ iterations.
- Mid-stream provider failure surfaces as error/blocked, never as success.
- No secret from `.env` appears in session JSON unredacted.
