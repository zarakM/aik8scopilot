package ai

// We call the Anthropic API directly using Go's standard net/http package.
// No special SDK needed — the API is just JSON over HTTPS.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"kubectl-ai/pkg/k8s"
)

const (
	claudeAPIURL = "https://api.anthropic.com/v1/messages"
	claudeModel  = "claude-sonnet-4-20250514"
)

// ClaudeClient holds the API key and the HTTP client.
// http.Client is safe for concurrent use and should be reused — not created per request.
type ClaudeClient struct {
	apiKey     string
	httpClient *http.Client
}

// These structs mirror the Anthropic API's JSON shape.
// json:"field_name" tells Go's JSON encoder/decoder what key name to use.
// omitempty means the field is skipped if empty (useful for optional fields).
type claudeRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewClaudeClient(apiKey string) *ClaudeClient {
	return &ClaudeClient{
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

// Diagnose sends the diagnostic data to Claude and returns the analysis.
func (c *ClaudeClient) Diagnose(ctx context.Context, data *k8s.DiagnosticData) (string, error) {
	reqBody := claudeRequest{
		Model:     claudeModel,
		MaxTokens: 1024,
		System:    systemPrompt(),
		Messages: []message{
			{Role: "user", Content: buildPrompt(data)},
		},
	}

	// json.Marshal converts the struct to a JSON byte slice.
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}

	// bytes.NewReader wraps a byte slice in an io.Reader interface,
	// which is what http.NewRequest expects for the body.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	// defer runs when the surrounding function returns — critical for closing
	// the response body to avoid leaking the HTTP connection.
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result claudeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse API response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("Claude API error (%s): %s", result.Error.Type, result.Error.Message)
	}

	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response from Claude")
	}

	return result.Content[0].Text, nil
}

// DiagnosePending sends pending-pod diagnostic data to Claude and returns the analysis.
// It uses a scheduling-focused system prompt instead of the crash-focused one.
func (c *ClaudeClient) DiagnosePending(ctx context.Context, data *k8s.PendingDiagnosticData) (string, error) {
	reqBody := claudeRequest{
		Model:     claudeModel,
		MaxTokens: 1024,
		System:    pendingSystemPrompt(),
		Messages: []message{
			{Role: "user", Content: buildPendingPrompt(data)},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result claudeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse API response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("Claude API error (%s): %s", result.Error.Type, result.Error.Message)
	}

	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response from Claude")
	}

	return result.Content[0].Text, nil
}

// pendingSystemPrompt is tuned for scheduling failures, not runtime crashes.
// The evidence that matters is totally different: node taints, quota limits, PVC binding.
func pendingSystemPrompt() string {
	return `You are an expert Kubernetes scheduler and SRE with 10+ years of production experience.

You will be given diagnostic data for a pod stuck in Pending state: the pod spec, scheduler events, node capacity and taints, namespace resource quotas, and PVC binding status.

Your job is to identify exactly why the scheduler cannot place this pod and tell the engineer how to fix it.

Always respond in this exact format — no preamble, start immediately with the first heading:

## 🔴 Root Cause
One clear sentence. Why can't the scheduler place this pod?

## 📊 Confidence
High / Medium / Low — one sentence explaining why.

## 🔍 Evidence
2–3 bullet points directly quoted or referenced from the data that support your diagnosis.

## 🔢 Probable Causes (ranked by likelihood)
1. Most likely: brief explanation
2. Second possibility: brief explanation
3. Third possibility (only if genuinely plausible)

## ⚡ Next Command
The single most useful kubectl command to run right now to confirm the root cause.
Format: ` + "`kubectl ...`" + `

## 🔧 Fix
Concrete action to resolve. Be specific:
- If nodes lack capacity, show the resource request values to reduce or the node pool to scale.
- If a taint blocks scheduling, show the exact toleration YAML to add to the pod spec.
- If a quota is exhausted, show which resource is over limit and how to raise it.
- If a PVC is unbound, explain why and how to fix the StorageClass or provisioner.

Do not hedge. Do not say "it could be many things." Pick the most likely cause and commit to it.`
}

// buildPendingPrompt constructs the user message for a pending pod diagnosis.
func buildPendingPrompt(data *k8s.PendingDiagnosticData) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Diagnose this pending Kubernetes pod: %s (namespace: %s)\n\n", data.PodName, data.Namespace))

	sb.WriteString("## Pod Spec\n```\n")
	sb.WriteString(data.PodSpec)
	sb.WriteString("```\n\n")

	// Events are the highest-signal input for pending pods — the scheduler writes
	// the exact reason it couldn't place the pod here.
	sb.WriteString("## Kubernetes Events\n```\n")
	sb.WriteString(data.Events)
	sb.WriteString("\n```\n\n")

	sb.WriteString("## Node Summary (capacity, allocatable, taints)\n```\n")
	sb.WriteString(data.NodeSummary)
	sb.WriteString("```\n\n")

	sb.WriteString("## Namespace Resource Quotas\n```\n")
	sb.WriteString(data.QuotaSummary)
	sb.WriteString("```\n\n")

	sb.WriteString("## PersistentVolumeClaim Status\n```\n")
	sb.WriteString(data.PVCSummary)
	sb.WriteString("\n```\n\n")

	sb.WriteString("Provide a structured diagnosis following the format in your instructions.")

	return sb.String()
}

// systemPrompt defines Claude's role and the exact output format we want.
// Getting this prompt right is the core product work — the Go code is just plumbing.
func systemPrompt() string {
	return `You are an expert Kubernetes SRE with 10+ years of production incident experience.

You will be given diagnostic data from a failing Kubernetes pod: the pod spec, container statuses, recent events, and logs.

Your job is to diagnose the issue and tell the engineer exactly what is wrong and how to fix it.

Always respond in this exact format — no preamble, start immediately with the first heading:

## 🔴 Root Cause
One clear sentence. What is broken right now.

## 📊 Confidence
High / Medium / Low — one sentence explaining why.

## 🔍 Evidence
2–3 bullet points directly quoted or referenced from the data that support your diagnosis.

## 🔢 Probable Causes (ranked by likelihood)
1. Most likely: brief explanation
2. Second possibility: brief explanation
3. Third possibility (only if genuinely plausible)

## ⚡ Next Command
The single most useful kubectl command to run right now to confirm the root cause.
Format: ` + "`kubectl ...`" + `

## 🔧 Fix
Concrete action to resolve. Be specific:
- If it's a config change, show the exact YAML field and value.
- If it's a command, show the exact command.
- If it's a resource limit issue, show the corrected values.

Do not hedge. Do not say "it could be many things." Pick the most likely cause and commit to it.`
}

// buildPrompt constructs the user message with all the diagnostic context.
// The quality of this prompt directly determines the quality of the diagnosis.
func buildPrompt(data *k8s.DiagnosticData) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Diagnose this failing Kubernetes pod: %s (namespace: %s)\n\n", data.PodName, data.Namespace))

	// Pod spec
	sb.WriteString("## Pod Spec\n```\n")
	sb.WriteString(data.PodSpec)
	sb.WriteString("```\n\n")

	// Container runtime status
	if len(data.Containers) > 0 {
		sb.WriteString("## Container Status\n")
		for _, cs := range data.Containers {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", cs.Name, cs.State))
			sb.WriteString(fmt.Sprintf("  Restarts: %d | Ready: %v\n", cs.RestartCount, cs.Ready))
			if cs.LastState != "" {
				sb.WriteString(fmt.Sprintf("  Last crash: %s\n", cs.LastState))
			}
		}
		sb.WriteString("\n")
	}

	// Events (these are gold — often contain the most direct explanation)
	sb.WriteString("## Kubernetes Events\n```\n")
	sb.WriteString(data.Events)
	sb.WriteString("\n```\n\n")

	// Logs
	if data.Logs != "" {
		sb.WriteString(fmt.Sprintf("## Container Logs (last %d lines)\n```\n", data.LogLineCount))
		sb.WriteString(data.Logs)
		sb.WriteString("\n```\n\n")
	} else {
		sb.WriteString("## Container Logs\nNo logs available (pod may not have started).\n\n")
	}

	sb.WriteString("Provide a structured diagnosis following the format in your instructions.")

	return sb.String()
}
