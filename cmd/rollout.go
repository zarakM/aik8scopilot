package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"kubectl-ai/pkg/ai"
	"kubectl-ai/pkg/k8s"
)

var rolloutCmd = &cobra.Command{
	Use:   "rollout <deployment-name>",
	Short: "Diagnose a stuck Deployment rollout using AI",
	Long: `Gathers the Deployment's status, ReplicaSets, owned pods (with logs from the worst one),
combined events, and matching PodDisruptionBudgets, then asks Claude for a root cause and fix.

The ANTHROPIC_API_KEY environment variable must be set.`,

	Args: cobra.ExactArgs(1),
	RunE: runRollout,
}

func init() {
	rootCmd.AddCommand(rolloutCmd)
	rolloutCmd.Flags().IntP("lines", "l", 50, "Number of log lines to fetch from the worst pod's containers")
	// --no-telemetry is plumbed for symmetry with `diagnose`, but rollout telemetry
	// is intentionally not wired this PR — will be added alongside pending telemetry later.
	rolloutCmd.Flags().Bool("no-telemetry", false, "Disable anonymous usage telemetry for this run (currently a no-op for rollout)")
}

func runRollout(cmd *cobra.Command, args []string) error {
	deploymentName := args[0]

	namespace, _ := cmd.Flags().GetString("namespace")
	logLines, _ := cmd.Flags().GetInt("lines")
	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is not set\n\nRun: export ANTHROPIC_API_KEY=your-key-here")
	}

	ctx := context.Background()

	fmt.Printf("\n🔍 Fetching rollout diagnostics for deployment %q in namespace %q...\n", deploymentName, namespace)

	client, err := k8s.NewClient(kubeconfig)
	if err != nil {
		return fmt.Errorf("could not connect to cluster: %w\n\nIs your kubeconfig set up? Try: kubectl get deployments", err)
	}

	data, err := client.GatherRolloutDiagnostics(ctx, namespace, deploymentName, logLines)
	if err != nil {
		return fmt.Errorf("failed to gather rollout data: %w", err)
	}

	fmt.Println("─────────────────────────────────────────────")
	fmt.Printf("✅ Collected deployment status, %d pods, %d events\n", data.PodCount, data.EventCount)
	fmt.Println("🤖 Sending to Claude for analysis...")
	fmt.Println()

	claudeClient := ai.NewClaudeClient(apiKey)
	if err := claudeClient.DiagnoseRollout(ctx, data, os.Stdout); err != nil {
		return fmt.Errorf("AI diagnosis failed: %w", err)
	}

	fmt.Println("─────────────────────────────────────────────")
	return nil
}
