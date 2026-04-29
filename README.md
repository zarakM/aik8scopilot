# kubectl-ai — AI Kubernetes Co-Pilot

> AI-native SRE that diagnoses Kubernetes failures in plain English

## What it does

One command — `diagnose` — works on any broken pod. It detects the failure type automatically:

- **Crashing pod** (CrashLoopBackOff, OOMKilled) — correlates logs, events, and resource limits
- **Pending pod** — correlates node capacity, taints, quotas, and PVC binding status
- Streams the diagnosis token-by-token as it arrives
- Returns structured output: root cause, confidence, evidence, exact fix command

## Requirements

- Go 1.21+
- A kubeconfig with access to your cluster
- An [Anthropic API key](https://console.anthropic.com/)

## Install

```bash
go install github.com/zarakm/aik8scopilot@latest
```

Or build from source:

```bash
git clone https://github.com/zarakm/aik8scopilot
cd aik8scopilot
go build -o kubectl-ai .
cp kubectl-ai /usr/local/bin/kubectl-ai
```

Cross-compile for Linux (e.g. a jump server):

```bash
GOOS=linux GOARCH=amd64 go build -o kubectl-ai-linux .
```

## Usage

```bash
export ANTHROPIC_API_KEY=your-key

# Works on any broken pod — auto-detects crash vs pending
kubectl-ai diagnose <pod-name> -n <namespace>

# Use a specific kubeconfig
kubectl-ai --kubeconfig ./my-kubeconfig diagnose <pod-name> -n <namespace>

# Disable telemetry for a single run
kubectl-ai diagnose <pod-name> --no-telemetry
```

## Telemetry

kubectl-ai collects anonymous usage data to improve diagnosis quality — error type, sanitized container states, event reasons, Claude's response, and a hashed cluster fingerprint. **No pod names, namespace names, env var values, or secret values are stored.**

To opt out, pass `--no-telemetry` on any run.

## Status

Early MVP — feedback welcome. Open an issue or DM on LinkedIn.
