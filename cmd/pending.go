package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"kubectl-ai/pkg/ai"
	"kubectl-ai/pkg/k8s"
)

var pendingCmd = &cobra.Command{
	Use:   "pending <pod-name>",
	Short: "Diagnose a pending pod using AI",
	Long: `Fetches scheduler events, node capacity, resource quotas, and PVC status
from your cluster, then asks Claude to diagnose why the pod cannot be scheduled.

The ANTHROPIC_API_KEY environment variable must be set.`,

	Args: cobra.ExactArgs(1),
	RunE: runPending,
}

func init() {
	rootCmd.AddCommand(pendingCmd)
}

func runPending(cmd *cobra.Command, args []string) error {
	podName := args[0]

	namespace, _ := cmd.Flags().GetString("namespace")
	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is not set\n\nRun: export ANTHROPIC_API_KEY=your-key-here")
	}

	ctx := context.Background()

	fmt.Printf("\n🔍 Fetching scheduling diagnostics for pod %q in namespace %q...\n", podName, namespace)

	client, err := k8s.NewClient(kubeconfig)
	if err != nil {
		return fmt.Errorf("could not connect to cluster: %w\n\nIs your kubeconfig set up? Try: kubectl get pods", err)
	}

	data, err := client.GatherPendingDiagnostics(ctx, namespace, podName)
	if err != nil {
		return fmt.Errorf("failed to gather pending pod data: %w", err)
	}

	fmt.Printf("✅ Collected %d events, node summary, quotas, and PVC status\n", data.EventCount)
	fmt.Println("🤖 Sending to Claude for analysis...\n")
	fmt.Println("─────────────────────────────────────────────")

	claudeClient := ai.NewClaudeClient(apiKey)
	if err := claudeClient.DiagnosePending(ctx, data, os.Stdout); err != nil {
		return fmt.Errorf("AI diagnosis failed: %w", err)
	}

	fmt.Println("─────────────────────────────────────────────")

	return nil
}
