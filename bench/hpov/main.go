// Command hpov is the HPOV benchmark runner: reproducible,
// distribution-first measurement of Forcefield launch, startup, and
// memory cost. Same Go module as ff (shares no product code paths);
// `go build .` on ff never compiles this.
//
// Usage:
//
//	hpov list [--tier N] [--kind K] [--here] [--long]
//	hpov env [--json]
//	hpov run --ff label=path ... [--select GLOB,...] [--tier N] [--profile P]
//	    [--n N] [--warmup N] [--seed S] --out result.json
//	hpov show [--metric GLOB] result.json
//	hpov validate result.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/envinfo"
	"forcefield/internal/hpov/report"
	"forcefield/internal/hpov/runner"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/suites"
)

// Exit codes: 0 completed (skips allowed), 1 usage,
// 2 >=1 benchmark error/invalid, 4 run quality poor with
// --require-quality good. (3 is the compare regression gate.)
const (
	exitOK      = 0
	exitUsage   = 1
	exitBench   = 2
	exitQuality = 4
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__noop" {
		// Spawn-floor calibration re-exec: trivial exit, no output.
		return
	}
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	self, _ := os.Executable()
	all := registeredBenchmarks(self)
	switch args[0] {
	case "list":
		return cmdList(args[1:], all)
	case "env":
		return cmdEnv(args[1:])
	case "run":
		return cmdRun(args[1:], all)
	case "show":
		return cmdShow(args[1:])
	case "validate":
		return cmdValidate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hpov: unknown command %q\n", args[0])
		usage()
		return exitUsage
	}
}

func registeredBenchmarks(self string) []bench.Benchmark {
	var out []bench.Benchmark
	for _, r := range suites.All() {
		out = append(out, r.Bench)
	}
	out = append(out, runner.NoopBenchmark(self))
	return out
}

func usage() {
	fmt.Fprint(os.Stderr, `hpov — Forcefield performance benchmarks
  hpov list [--tier N] [--kind e2e|micro|static] [--here] [--long]
  hpov env [--json]
  hpov run --ff label=path [--ff ...] [--select GLOB,...] [--exclude GLOB,...]
           [--tier N] [--kind K] [--profile quick|standard|full]
           [--n N] [--warmup N] [--seed S] [--source release|local-build]
           [--fail-fast] [--require-quality good] [--workroot DIR] --out result.json
  hpov show [--metric GLOB] result.json
  hpov validate result.json
`)
}

// ffList is a repeatable --ff label=path flag.
type ffList []bench.Subject

func (f *ffList) String() string { return fmt.Sprint(*f) }

func (f *ffList) Set(v string) error {
	label, path, ok := strings.Cut(v, "=")
	if !ok || label == "" || path == "" {
		return fmt.Errorf("want --ff label=path, got %q", v)
	}
	*f = append(*f, bench.Subject{Label: label, Path: path})
	return nil
}

func cmdList(args []string, all []bench.Benchmark) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	tier := fs.Int("tier", -1, "filter by tier")
	kind := fs.String("kind", "", "filter by kind")
	here := fs.Bool("here", false, "only benchmarks runnable on this host")
	long := fs.Bool("long", false, "print purpose text")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	have := runner.Available()
	specs := make([]bench.Spec, 0, len(all))
	for _, b := range all {
		s := b.Spec()
		if *tier >= 0 && s.Tier != *tier {
			continue
		}
		if *kind != "" && string(s.Kind) != *kind {
			continue
		}
		if *here {
			if ok, _ := s.RunnableOn(runtime.GOOS, have); !ok {
				continue
			}
		}
		specs = append(specs, s)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	for _, s := range specs {
		ok, reason := s.RunnableOn(runtime.GOOS, have)
		state := "runnable"
		if !ok {
			state = reason
		}
		fmt.Printf("%-34s tier=%d kind=%-6s %s\n", s.ID, s.Tier, string(s.Kind), state)
		if *long {
			fmt.Printf("    %s\n", s.Purpose)
		}
	}
	return exitOK
}

func cmdEnv(args []string) int {
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	// env collects from the process temp dir (no run root yet).
	info, err := envinfo.Collect(os.TempDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "hpov env: %v\n", err)
		return exitBench
	}
	if *asJSON {
		raw, _ := json.MarshalIndent(info.Host, "", "  ")
		fmt.Println(string(raw))
		return exitOK
	}
	fmt.Printf("os=%s/%s env=%s cpus=%d clock=%s(%dns) idle=%.1f%% power=%s/%s fs=%s git=%s rg=%s go=%s\n",
		info.Host.OS, info.Host.Arch, info.Host.Env, info.Host.CPU.Logical,
		info.Host.Clock.Source, info.Host.Clock.ResolutionNs,
		zeroOr(info.IdleCPUPct), info.Host.Power.Source, info.Host.Power.Plan,
		info.Host.FS.Workroot, strOr(info.Host.Tools.Git), strOr(info.Host.Tools.Rg), strOr(info.Host.Tools.Go))
	return exitOK
}

func cmdRun(args []string, all []bench.Benchmark) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var subjects ffList
	fs.Var(&subjects, "ff", "subject binary as label=path (repeatable)")
	selectS := fs.String("select", "", "comma-separated ID globs")
	excludeS := fs.String("exclude", "", "comma-separated ID globs")
	tier := fs.Int("tier", 1, "tier to run (-1 = all)")
	kind := fs.String("kind", "", "kind filter (applies when --select is empty)")
	profile := fs.String("profile", "standard", "quick|standard|full")
	n := fs.Int("n", 0, "override measured iterations (0 = plan)")
	warmup := fs.Int("warmup", -1, "override warm-up iterations (-1 = plan)")
	seed := fs.Int64("seed", 424242, "run seed (interleave order, bootstrap)")
	source := fs.String("source", "local-build", "subject provenance release|local-build")
	out := fs.String("out", "", "output result.json (required)")
	workroot := fs.String("workroot", "", "filesystem root for fixtures (default os temp)")
	failFast := fs.Bool("fail-fast", false, "stop after the first error/invalid benchmark")
	requireQuality := fs.String("require-quality", "", "gate: good (exit 4 when run quality is poor)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "hpov run: --out is required")
		return exitUsage
	}
	cfg := runner.Config{
		Benchmarks:    all,
		Subjects:      []bench.Subject(subjects),
		SubjectSource: *source,
		Select:        splitList(*selectS),
		Exclude:       splitList(*excludeS),
		Tier:          *tier,
		Kind:          *kind,
		Profile:       *profile,
		Seed:          *seed,
		Out:           *out,
		WorkRoot:      *workroot,
		FailFast:      *failFast,
		CommandLine:   os.Args,
	}
	if *n > 0 {
		cfg.N = n
	}
	if *warmup >= 0 {
		cfg.Warmup = warmup
	}
	outcome, err := runner.Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hpov run: %v\n", err)
		return exitBench
	}
	fmt.Print(report.Render(outcome.Result))
	fmt.Printf("wrote %s\n", outcome.Path)
	if outcome.BenchmarkErrors {
		return exitBench
	}
	if *requireQuality == "good" && outcome.Result.Run.Quality.Label == "poor" {
		return exitQuality
	}
	return exitOK
}

func cmdShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	metric := fs.String("metric", "", "only metrics matching GLOB")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "hpov show: want result.json")
		return exitUsage
	}
	r, problems, err := schema.ValidateFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hpov show: %v\n", err)
		return exitBench
	}
	if *metric != "" {
		filtered := *r
		filtered.Benchmarks = nil
		for _, b := range r.Benchmarks {
			nb := b
			nb.Metrics = nil
			for _, m := range b.Metrics {
				if bench.Match(*metric, b.ID+"."+m.Name) || bench.Match(*metric, m.Name) {
					nb.Metrics = append(nb.Metrics, m)
				}
			}
			if len(nb.Metrics) > 0 {
				filtered.Benchmarks = append(filtered.Benchmarks, nb)
			}
		}
		r = &filtered
	}
	fmt.Print(report.Render(r))
	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "schema problems:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		return exitBench
	}
	return exitOK
}

func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "hpov validate: want result.json")
		return exitUsage
	}
	_, problems, err := schema.ValidateFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hpov validate: %v\n", err)
		return exitBench
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		return exitBench
	}
	fmt.Println("valid")
	return exitOK
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func strOr(p *string) string {
	if p == nil {
		return "-"
	}
	return *p
}

func zeroOr(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
