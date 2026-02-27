# Contributing to ThreadGraph

## Running the test suite

```bash
go build -o /tmp/threadgraph .
/tmp/threadgraph run --no-llm ./testdata/buggy/           # → 5 goroutine leaks
/tmp/threadgraph run --no-llm ./testdata/chan-receive-leak/   # → 1 goroutine leak
```

## Adding a new bug testcase

1. Create `testdata/<project>-<issue>/bug.go` with the buggy code
2. Create `testdata/<project>-<issue>/bug_test.go` with a test that triggers it
3. Verify: `threadgraph run --no-llm ./testdata/<project>-<issue>/`

## Running the full GoBench benchmark

Requires `/tmp/gobench/` (clone from [GoBench](https://github.com/timmyyuan/gobench)) and `/tmp/run_gobench.sh`.

```bash
bash /tmp/run_gobench.sh
```

## Discussion

Open a GitHub Issue for bug reports or feature ideas.
