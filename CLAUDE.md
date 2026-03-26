# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is
A kubectl plugin (startup product) that diagnoses Kubernetes failures in real time using Claude.
Fetches pod logs, events, and spec → calls Anthropic API → returns plain-English root cause + fix.

## Build and run
```bash
go mod tidy
go build -o kubectl-ai .
export ANTHROPIC_API_KEY=...
./kubectl-ai diagnose <pod-name> -n <namespace>
./kubectl-ai pending <pod-name> -n <namespace>
```

Production build with telemetry baked in (required to activate Supabase logging):
```bash
GOOS=linux GOARCH=amd64 go build -ldflags "\
  -X kubectl-ai/pkg/telemetry.supabaseURL=https://yourproject.supabase.co \
  -X kubectl-ai/pkg/telemetry.supabaseKey=your-anon-key" \
  -o kubectl-ai-linux .
```

Dev override (skip recompile): set `SUPABASE_URL` + `SUPABASE_KEY` env vars — they take precedence over ldflags values.

## Architecture

### Request flow
```
cmd/<command>.go
  → k8s.NewClient(kubeconfig)                  # builds clientset from kubeconfig
  → client.Gather*Diagnostics(ctx, ...)        # all cluster API calls, returns structured data
  → io.MultiWriter(os.Stdout, &diagBuf)        # tees streaming output for telemetry capture
  → ai.NewClaudeClient(apiKey).Diagnose*(...)  # builds prompt, streams SSE response token-by-token
  → telemetry.LogIncident(...)                 # fire-and-forget goroutine POST to Supabase
```

### Package responsibilities
- `cmd/` — orchestration only; no business logic. Each command follows the same pattern: gather → stream → log.
- `pkg/k8s/client.go` — all `client-go` calls. Two diagnostic data types: `DiagnosticData` (crash) and `PendingDiagnosticData` (scheduling). `formatPodSpec` strips secret values.
- `pkg/ai/claude.go` — prompt construction + streaming. `streamTo()` is shared by all `Diagnose*` methods. System prompts enforce a strict output format (Root Cause / Confidence / Evidence / Probable Causes / Next Command / Fix).
- `pkg/telemetry/logger.go` — silent background logging to Supabase. `supabaseURL`/`supabaseKey` are injected via `-ldflags`; env vars override for local dev. Never blocks the CLI. `--no-telemetry` flag on diagnose disables per-run.

### Telemetry data model (Supabase `incidents` table)
Seven fields: `error_type`, `signals` (jsonb — sanitized container states + event reasons + log tail), `diagnosis`, `confidence`, `cluster_id` (SHA256 of server URL, first 8 bytes), `model`, `created_at`. No pod names, namespace names, env var values, or secret names are stored.

## MVP scope — 3 commands only
1. `diagnose` — CrashLoopBackOff ✅
2. `pending` — Pending pods ✅
3. `rollout` — Stuck deployment rollouts (not yet built)

Do NOT expand scope beyond these three.

## Key decisions
- Direct HTTP to Anthropic — no SDK, intentional. Do not introduce LLM frameworks.
- Streaming via SSE (`bufio.Scanner` line-by-line) — `streamTo()` in `claude.go` is the single implementation.
- Anthropic key = user's own (`ANTHROPIC_API_KEY`). Supabase keys = yours, baked in via ldflags.
- `pending.go` does not yet have telemetry wired — follow the `diagnose.go` pattern when adding it.

## Code style
- Errors wrapped with `%w`, returned to `cmd/` layer for printing
- `context.Context` in every function that does I/O
- No global state — pass dependencies explicitly
- Comment the WHY, not the WHAT

## Do not
- Do not add a web server, database, or persistence layer
- Do not add authentication or multi-tenancy
- Do not change system prompts in `claude.go` without testing against real K8s errors
- Do not store pod names, namespace names, env var values, or actual cluster URLs in telemetry
