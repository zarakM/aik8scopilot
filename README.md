# aik8scopilot — AI Kubernetes Co-Pilot

> AI-native SRE that diagnoses Kubernetes failures in plain English

## What it does
- Diagnoses CrashLoopBackOff, pending pods, and stuck rollouts
- Fetches pod logs, events, and spec automatically
- Returns root cause + exact fix command in seconds

## Install
```bash
go install github.com/yourusername/aik8scopilot@latest
```

## Usage
```bash
export ANTHROPIC_API_KEY=your-key
kubectl-ai diagnose <pod-name> -n <namespace>
```

## Status
Early MVP — feedback welcome. Open an issue or DM on LinkedIn.
