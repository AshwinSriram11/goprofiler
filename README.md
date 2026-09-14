# goprofiler

A scratch space for learning and testing Go's profiling tools (`pprof`, `trace`,
runtime metrics, etc.) through small, self-contained experiments.

## Layout

Each experiment lives in its own directory under `cmd/`, as an independent
`main` package:

```
cmd/
  leaky-slice/    # unbounded slice growth + goroutine block, pprof over HTTP
```

Run one with:

```sh
go run ./cmd/<experiment-name>
```

Shared helpers (if/when they emerge) will live under `internal/`.

## Adding a new experiment

```sh
mkdir -p cmd/<name>
# write cmd/<name>/main.go
go run ./cmd/<name>
```
