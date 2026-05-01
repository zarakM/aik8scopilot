package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"kubectl-ai/pkg/ai"
	"kubectl-ai/pkg/k8s"
	"kubectl-ai/pkg/telemetry"
)

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose <pod-name>",
	Short: "Diagnose a failing pod using AI",
	Long: `Automatically detects whether the pod is pending or crashing,
fetches the relevant diagnostics, and asks Claude for a root cause and fix.

The ANTHROPIC_API_KEY environment variable must be set.`,

	Args: cobra.ExactArgs(1),
	RunE: runDiagnose,
}

func init() {
	rootCmd.AddCommand(diagnoseCmd)
	diagnoseCmd.Flags().IntP("lines", "l", 50, "Number of log lines to fetch per container")
	diagnoseCmd.Flags().Bool("no-telemetry", false, "Disable anonymous usage telemetry for this run")
}

func runDiagnose(cmd *cobra.Command, args []string) error {
	podName := args[0]

	namespace, _ := cmd.Flags().GetString("namespace")
	logLines, _ := cmd.Flags().GetInt("lines")
	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")
	noTelemetry, _ := cmd.Flags().GetBool("no-telemetry")

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is not set\n\nRun: export ANTHROPIC_API_KEY=your-key-here")
	}

	ctx := context.Background()

	fmt.Printf("\n🔍 Fetching diagnostics for pod %q in namespace %q...\n", podName, namespace)

	client, err := k8s.NewClient(kubeconfig)
	if err != nil {
		return fmt.Errorf("could not connect to cluster: %w\n\nIs your kubeconfig set up? Try: kubectl get pods", err)
	}

	// Detect pod phase first so we can choose the right diagnostic path.
	// Pending pods have no logs — they need scheduler/node/quota data instead.
	phase, err := client.GetPodPhase(ctx, namespace, podName)
	if err != nil {
		return fmt.Errorf("failed to fetch pod: %w", err)
	}

	var diagBuf bytes.Buffer
	out := io.MultiWriter(os.Stdout, &diagBuf)
	claudeClient := ai.NewClaudeClient(apiKey)

	fmt.Println("─────────────────────────────────────────────")

	var (
		crashData   *k8s.DiagnosticData
		pendingData *k8s.PendingDiagnosticData
	)

	if phase == "Pending" {
		pendingData, err = runPendingDiagnosis(ctx, client, claudeClient, namespace, podName, out)
	} else {
		crashData, err = runCrashDiagnosis(ctx, client, claudeClient, namespace, podName, logLines, out)
	}
	if err != nil {
		return err
	}

	fmt.Println("─────────────────────────────────────────────")

	if !noTelemetry {
		diagnosis := diagBuf.String()
		serverURL := client.ServerURL()
		if pendingData != nil {
			telemetry.LogPendingIncident(pendingData, diagnosis, serverURL)
		} else if crashData != nil {
			telemetry.LogCrashIncident(crashData, diagnosis, serverURL)
		}
	}

	return nil
}

func runCrashDiagnosis(ctx context.Context, client *k8s.Client, claudeClient *ai.ClaudeClient,
	namespace, podName string, logLines int, out io.Writer) (*k8s.DiagnosticData, error) {

	data, err := client.GatherDiagnostics(ctx, namespace, podName, logLines)
	if err != nil {
		return nil, fmt.Errorf("failed to gather pod data: %w", err)
	}

	fmt.Fprintf(out, "✅ Collected %d log lines, %d events, and pod spec\n", data.LogLineCount, data.EventCount)
	fmt.Fprintln(out, "🤖 Sending to Claude for analysis...")
	fmt.Fprintln(out)

	if err := claudeClient.Diagnose(ctx, data, out); err != nil {
		return nil, fmt.Errorf("AI diagnosis failed: %w", err)
	}
	return data, nil
}

func runPendingDiagnosis(ctx context.Context, client *k8s.Client, claudeClient *ai.ClaudeClient,
	namespace, podName string, out io.Writer) (*k8s.PendingDiagnosticData, error) {

	data, err := client.GatherPendingDiagnostics(ctx, namespace, podName)
	if err != nil {
		return nil, fmt.Errorf("failed to gather pending pod data: %w", err)
	}

	fmt.Fprintf(out, "✅ Collected %d events, node summary, quotas, and PVC status\n", data.EventCount)
	fmt.Fprintln(out, "🤖 Sending to Claude for analysis...")
	fmt.Fprintln(out)

	if err := claudeClient.DiagnosePending(ctx, data, out); err != nil {
		return nil, fmt.Errorf("AI diagnosis failed: %w", err)
	}

	return data, nil
}
