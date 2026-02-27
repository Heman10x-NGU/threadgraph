# ThreadGraph

> Find goroutine leaks and deadlocks in Go programs — before they hit production.

[![CI](https://github.com/Heman10x-NGU/threadgraph/actions/workflows/ci.yml/badge.svg)](https://github.com/Heman10x-NGU/threadgraph/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8.svg)](go.mod)
[![GoBench](https://img.shields.io/badge/GoBench%20GoKer-62%2F68%20(91%25)-brightgreen)](GOBENCH_RESULTS.md)

---

Goroutine leaks and deadlocks are among the hardest bugs in production Go.
They're invisible in normal testing, survive code review, and only surface
under specific load — causing cascading memory growth or complete service hangs.

ThreadGraph analyzes Go execution traces to find exactly which goroutines are
permanently blocked, what they're blocked on, and where they were created —
with no binary modification, no code changes, and no instrumentation.

---

## Install

```bash
go install github.com/Heman10x-NGU/threadgraph@latest
```

## Quick Usage

```bash
# Auto-capture trace and analyze
threadgraph run ./...

# Analyze an existing trace
threadgraph analyze trace.out

# With static lock-release analysis
threadgraph run --static ./...

# CI-friendly JSON output
threadgraph run --format json --no-llm ./...
```

## Demo

```
ThreadGraph Analysis
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  5 goroutine leaks
  0 deadlocks
  0 long blocks

● GOROUTINE LEAK  (high confidence)
  Goroutine 18 blocked on: "chan receive"
  Location: testdata/buggy/main.go:42
  Stack:
    main.leakyWorker
    testdata/buggy/main.go:42 +0x28

● GOROUTINE LEAK  (high confidence)
  Goroutine 22 blocked on: "chan receive"
  Location: testdata/buggy/main.go:42
  Stack:
    main.leakyWorker
    testdata/buggy/main.go:42 +0x28

  [3 more leaks...]

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Analyzed 47 goroutines · 2341ms window · /tmp/trace123.out
```

## What It Detects

| Finding Type           | How Detected                              | Confidence |
|------------------------|-------------------------------------------|------------|
| Goroutine leak         | Channel-blocked at trace end              | High       |
| Mutex deadlock         | 2+ goroutines at same lock site >500ms    | Medium     |
| AB-BA lock inversion   | Crossed lock acquisition history          | Medium     |
| Channel-lock cycle     | Lock holder blocked on channel            | Medium     |
| Lock leak (static)     | go/ssa CFG: lock path without unlock      | Low        |
| Orphan goroutine       | Created but never scheduled               | Low        |

## GoBench Benchmark

Tested against [GoBench GoKer](https://github.com/timmyyuan/gobench) — 68 real blocking bugs extracted from production Go projects by researchers at UCSB (CGO 2021 paper).

**Projects tested**: Kubernetes, etcd, CockroachDB, gRPC, Docker, Hugo, Istio, Syncthing

| Version | Score | Key Technique Added |
|---------|-------|---------------------|
| v0.1    | 31/68 (45%) | Basic leak detection |
| v0.2    | 59/68 (86%) | AB-BA, chan+lock cycle, schedule diversity |
| v0.3    | 62/68 (91%) | `testing.T.Run` provenance fix, syncHistory |

**False positives on `net/http/httptest` (224 goroutines): 0**

No other open-source Go tool has been benchmarked against this dataset.

## How It Works

ThreadGraph runs `go test -trace` on your package — no binary modification needed.
It parses the Go execution trace (a structured binary log of every goroutine state
transition) and applies 6 detection algorithms:

- **detectLeaks** — goroutines blocked on channels at trace end
- **detectDeadlocks** — mutex contention groups by call site
- **detectABBA** — crossed lock-acquisition history across goroutines
- **detectChanLockCycle** — goroutines holding a lock while waiting on a channel
- **detectOrphans** — goroutines that never ran before the test exited
- **detectTransientBlocks** — mutex deadlocks unblocked by test timeout

If no bugs are found on the first pass, it automatically retries with GOMAXPROCS=1, 2,
and 4 to expose scheduling-dependent bugs that only manifest under specific interleavings.

For full algorithm documentation, see [ARCHITECTURE.md](ARCHITECTURE.md).

## AI Explanations

When `ANTHROPIC_API_KEY` is set, ThreadGraph calls Claude to explain each finding in
plain English and suggest a fix. Pass `--no-llm` to skip this.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
threadgraph run ./...
```

## Flags

```
--format string     Output format: terminal (default) or json
--no-llm            Skip Claude AI explanations
--output string     Write output to file instead of stdout
--min-block string  Minimum block duration to report (default "500ms")
--static            Enable go/ssa static lock-release analysis
--debug-filtered    Print all blocked goroutines with filter status to stderr
```

## Roadmap

- [ ] Goroutine provenance tree — eliminate false positives in server code
- [ ] Source code context in AI explanations (20-line window around finding)
- [ ] CI baseline comparison (`--save-baseline` / `--baseline`)
- [ ] VS Code extension with inline annotations
- [ ] GitHub PR check — "this PR introduced 2 goroutine leaks"
- [ ] Production sampling mode (LeakProf-style pprof for long-running services)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache 2.0 — see [LICENSE](LICENSE).
