# kubectl-ai — AI Kubernetes Co-Pilot

## What this is
A kubectl plugin that diagnoses Kubernetes failures in real time using Claude.
It fetches pod logs, events, and spec, then calls the Anthropic API to return a
plain-English root cause + fix. This is an MVP / startup product, not an internal tool.

## Project structure
```
main.go                  # thin entry point — hands off to cmd/
cmd/root.go              # cobra CLI root, persistent flags (--namespace, --kubeconfig)
cmd/diagnose.go          # `kubectl ai diagnose <pod>` command
pkg/k8s/client.go        # kubernetes client-go wrapper — all cluster API calls
pkg/ai/claude.go         # Anthropic API client — prompt construction + HTTP call
go.mod                   # dependencies: cobra, client-go
```

## Build and run
```bash
go mod tidy              # install dependencies
go build -o kubectl-ai . # compile binary
export ANTHROPIC_API_KEY=...
./kubectl-ai diagnose <pod-name> -n <namespace>
```

To use as a real kubectl plugin:
```bash
cp kubectl-ai /usr/local/bin/kubectl-ai
kubectl-ai diagnose <pod-name> -n <namespace>
```

## Key decisions already made
- Language: Go (not Python) — kubectl ecosystem, client-go, single binary distribution
- LLM: Anthropic Claude (claude-sonnet-4-20250514) via direct HTTP, no SDK
- CLI framework: cobra (same library kubectl uses)
- Auth: ANTHROPIC_API_KEY env var — never hardcode keys
- Kubeconfig: defaults to ~/.kube/config, overridable with --kubeconfig flag
- Log fetching: always fetches BOTH current logs AND previous crashed instance logs

## MVP scope — 3 pain points only
1. CrashLoopBackOff — correlate logs + events + resource limits
2. Pending pods — correlate node capacity + taints + quotas + PVC binding
3. Stuck/failing deployment rollouts — correlate readiness probes + events + new pod logs

Do NOT expand scope beyond these three until all three are solid.

## Code style
- All errors must be wrapped with %w and return up to cmd layer for printing
- Use context.Context in every function that does I/O
- No global state — pass dependencies explicitly
- Keep cmd/ thin: orchestration only. Logic lives in pkg/
- Comment the WHY, not the WHAT — the Go concepts are explained for a learning developer

## What's next to build
- `cmd/pending.go` — diagnose pending pods (same pattern as diagnose.go)
- `cmd/rollout.go` — diagnose stuck rollouts
- Streaming output (print Claude's response token by token as it arrives)
- Better error messages when cluster is unreachable
- `--output json` flag for machine-readable diagnosis

## Do not
- Do not add a web server, database, or any persistence layer yet
- Do not add authentication/multi-tenancy — this is a local CLI tool for now
- Do not use LangChain or any LLM framework — raw HTTP is intentional
- Do not change the prompt format in claude.go without testing against real K8s errors first
