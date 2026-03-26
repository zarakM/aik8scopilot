package telemetry

// Silent background telemetry — logs every diagnose run to Supabase.
// Never blocks the user; all errors are swallowed. Disabled if env vars are unset.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"kubectl-ai/pkg/k8s"
)

// IncidentLog is the seven-field row written to the `incidents` table.
type IncidentLog struct {
	ErrorType  string  `json:"error_type"`
	Signals    Signals `json:"signals"`
	Diagnosis  string  `json:"diagnosis"`
	Confidence string  `json:"confidence"`
	ClusterID  string  `json:"cluster_id"`
	Model      string  `json:"model"`
}

// Signals is the sanitized snapshot of what we sent to Claude.
// Rule: keep structure and error patterns, strip identity (names, values, URLs).
type Signals struct {
	Containers []ContainerSignal `json:"containers"`
	Events     []EventSignal     `json:"events"`
	// Log tail is kept because error stack traces are the core training signal.
	// Most apps do not log secrets; this is documented in the README.
	LogTail    string `json:"log_tail"`
	EventCount int    `json:"event_count"`
}

type ContainerSignal struct {
	State        string `json:"state"`         // e.g. "Waiting: CrashLoopBackOff"
	LastState    string `json:"last_state"`    // e.g. "Exit code 137 (OOMKilled)"
	RestartCount int32  `json:"restart_count"`
	Ready        bool   `json:"ready"`
}

type EventSignal struct {
	Type   string `json:"type"`   // "Normal" or "Warning"
	Reason string `json:"reason"` // e.g. "OOMKilling", "BackOff"
}

const claudeModel = "claude-sonnet-4-20250514"

// supabaseURL and supabaseKey are injected at build time via -ldflags.
// Example:
//
//	go build -ldflags "\
//	  -X kubectl-ai/pkg/telemetry.supabaseURL=https://yourproject.supabase.co \
//	  -X kubectl-ai/pkg/telemetry.supabaseKey=your-anon-key" \
//	  -o kubectl-ai .
//
// SUPABASE_URL / SUPABASE_KEY env vars override the compiled-in values,
// which is useful for testing against a dev project without rebuilding.
var (
	supabaseURL = "" // set via -ldflags at build time
	supabaseKey = "" // set via -ldflags at build time
)

// getSupabaseConfig returns the active URL and key, preferring env var overrides.
func getSupabaseConfig() (url, key string) {
	if u := os.Getenv("SUPABASE_URL"); u != "" {
		return u, os.Getenv("SUPABASE_KEY")
	}
	return supabaseURL, supabaseKey
}

// LogIncident fires a background POST to Supabase.
// Returns immediately — the HTTP call runs in a goroutine so the CLI is never blocked.
// Silently returns if neither ldflags nor env vars provide credentials.
func LogIncident(data *k8s.DiagnosticData, diagnosis, serverURL string) {
	url, key := getSupabaseConfig()
	if url == "" || key == "" {
		return
	}

	log := IncidentLog{
		ErrorType:  detectErrorType(data),
		Signals:    buildSignals(data),
		Diagnosis:  diagnosis,
		Confidence: extractConfidence(diagnosis),
		ClusterID:  AnonymizeCluster(serverURL),
		Model:      claudeModel,
	}

	// Fire and forget — context.Background so cancellation of the CLI context
	// doesn't abort the in-flight POST.
	// Capture url/key in the closure so the goroutine doesn't re-read env vars.
	go func(url, key string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		body, err := json.Marshal(log)
		if err != nil {
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			url+"/rest/v1/incidents", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("apikey", key)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Prefer", "return=minimal") // don't return the inserted row

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	}(url, key)
}

// AnonymizeCluster hashes the cluster API server URL.
// First 8 bytes (16 hex chars) is enough to distinguish clusters without storing real URLs.
func AnonymizeCluster(serverURL string) string {
	h := sha256.Sum256([]byte(serverURL))
	return fmt.Sprintf("%x", h[:8])
}

// detectErrorType inspects container states and event text to classify the failure.
func detectErrorType(data *k8s.DiagnosticData) string {
	for _, c := range data.Containers {
		if strings.Contains(c.State, "CrashLoopBackOff") {
			return "CrashLoopBackOff"
		}
		if strings.Contains(c.State, "OOMKilled") || strings.Contains(c.LastState, "OOMKilled") {
			return "OOMKilled"
		}
		if strings.Contains(c.State, "ImagePullBackOff") || strings.Contains(c.State, "ErrImagePull") {
			return "ImagePullError"
		}
	}
	// Fall back to event text — OOMKilling appears here before the state updates.
	if strings.Contains(data.Events, "OOMKilling") {
		return "OOMKilled"
	}
	if strings.Contains(data.Events, "BackOff") {
		return "CrashLoopBackOff"
	}
	return "Unknown"
}

// buildSignals extracts the structured, sanitized telemetry payload from DiagnosticData.
func buildSignals(data *k8s.DiagnosticData) Signals {
	s := Signals{
		EventCount: data.EventCount,
		LogTail:    truncate(data.Logs, 2000), // cap at 2 KB — enough for stack traces
	}

	for _, c := range data.Containers {
		s.Containers = append(s.Containers, ContainerSignal{
			State:        c.State,
			LastState:    c.LastState,
			RestartCount: c.RestartCount,
			Ready:        c.Ready,
		})
	}

	// Parse events from the formatted string "[Type] Reason: Message"
	// We keep Type and Reason only — Message can contain cluster-specific details.
	for _, line := range strings.Split(data.Events, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "No events found." {
			continue
		}
		// Format: [Warning] OOMKilling: ...
		if len(line) > 1 && line[0] == '[' {
			end := strings.Index(line, "]")
			if end > 0 {
				typ := line[1:end]
				rest := strings.TrimSpace(line[end+1:])
				reason := strings.SplitN(rest, ":", 2)[0]
				s.Events = append(s.Events, EventSignal{Type: typ, Reason: strings.TrimSpace(reason)})
			}
		}
	}

	return s
}

// extractConfidence reads the confidence level from Claude's structured response.
// The system prompt guarantees the format "## 📊 Confidence\nHigh / Medium / Low — ..."
func extractConfidence(diagnosis string) string {
	lines := strings.Split(diagnosis, "\n")
	for i, line := range lines {
		if strings.Contains(line, "Confidence") && i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			switch {
			case strings.HasPrefix(next, "High"):
				return "High"
			case strings.HasPrefix(next, "Medium"):
				return "Medium"
			case strings.HasPrefix(next, "Low"):
				return "Low"
			}
		}
	}
	return ""
}

// truncate caps a string at maxBytes to avoid sending huge payloads.
func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[len(s)-maxBytes:] // keep the tail — most recent output is most relevant
}