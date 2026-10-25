# goprofiler

A scratch space for learning and testing Go's profiling tools (`pprof`, `trace`,
runtime metrics, etc.) through small, self-contained experiments. It has grown
into a small pull-based profile collector plus a workload to profile.

## Layout

Each program lives in its own directory under `cmd/`, as an independent `main`
package:

```
cmd/
  leaky-slice/    # unbounded slice growth + goroutine block, pprof over HTTP
  workload/       # service to profile: CPU burn, heap churn/leak, goroutine leak
  collector/      # pulls pprof profiles from one or more targets and saves them
targets.json      # list of targets the collector pulls from
```

## Workload

`cmd/workload` serves pprof over HTTP (`-addr`, default `:6061`) and runs
these patterns in the background:

| Pattern | What it does | Visible in |
|---|---|---|
| `burnCPU` | Prime checks on random odd numbers in a loop | CPU profile |
| `steadyChurn` | Allocates and discards short-lived buffers | heap `alloc_space` climbs, `inuse_space` stays flat |
| `genuineLeak` | Retains every buffer it allocates | heap `inuse_space` grows without bound |
| `goroutineLeak` | Spawns a goroutine that blocks forever every 100ms | goroutine count grows without bound |

Run several instances on different ports to match `targets.json`:

```sh
go run ./cmd/workload -addr :6061
go run ./cmd/workload -addr :6062
go run ./cmd/workload -addr :6063
```

## Collector

`cmd/collector` pulls profiles from every target in a JSON config file and
writes them to disk as `<target>-<unix-ts>-<type>.pb.gz`.

```sh
go run ./cmd/collector -profiles cpu,heap,goroutine
```

| Flag | Default | Meaning |
|---|---|---|
| `-config` | `targets.json` | JSON file listing targets (`name`, `url`) |
| `-profiles` | `cpu` | Comma-separated profile types: `cpu`, `heap`, `goroutine` |
| `-seconds` | `10` | Sampling window for the CPU profile |
| `-out` | `./profiles` | Directory to write profile files to |
| `-interval` | `0` (once) | If set, collect repeatedly on this interval |

A failure on one target or profile type is logged and skipped; it doesn't stop
collection from the others. Collected profiles are git-ignored.

Inspect one with:

```sh
go tool pprof -http=:8081 profiles/<file>.pb.gz
```

## Running a single experiment

```sh
go run ./cmd/<name>
```

## Adding a new experiment

```sh
mkdir -p cmd/<name>
# write cmd/<name>/main.go
go run ./cmd/<name>
```

Shared helpers (if/when they emerge) will live under `internal/`.

Branch, commit and PR conventions are in [CONVENTIONS.md](CONVENTIONS.md).
