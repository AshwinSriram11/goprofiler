# Conventions

## Branches

One branch per change (experiment, feature, fix, test, doc), off `main`:

```
<type>/<short-kebab-description>
```

Types: `feat`, `experiment`, `fix`, `test`, `chore`, `docs`

Examples:
- `experiment/goroutine-leak`
- `feat/cpu-profile-cmd`
- `fix/leaky-slice-flag`
- `docs/pprof-notes`

## Commits

```
<type>: <imperative summary>

<optional body — only the non-obvious "why">
```

Examples:
- `experiment: add goroutine-leak profiling demo`
- `feat: add flags for pprof server address`
- `docs: add branching/commit conventions`

## Pull Requests

Title mirrors the commit type prefix, kept under ~70 characters:

```
<type>: <short summary>
```

Description:

```
## Summary
<1-3 bullet points — what changed, and why, at a higher level than the commits>

## Test plan
<bulleted checklist of how this was verified>
```

Examples:
- `feat: pull-based collector (Phase 1)`
- `docs: push vs pull architecture notes`

## Workflow

1. Branch off `main` for the change.
2. Commit in small, reviewable steps.
3. Push the branch.
4. Merge to `main` as its own separate, explicitly approved step.
