package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"kubectl-ai/pkg/ai"
	"kubectl-ai/pkg/k8s"
)

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose <pod-name>",
	Short: "Diagnose a failing pod using AI",
	Long: `Fetches pod logs, events, and spec from your cluster,
then asks Claude to diagnose the issue and suggest a fix.

The ANTHROPIC_API_KEY environment variable must be set.`,

	// ExactArgs(1) means cobra will error automatically if the user
	// forgets to pass a pod name or passes too many arguments.
	Args: cobra.ExactArgs(1),

	// RunE returns an error — preferred over Run because errors propagate
	// cleanly up to Execute() which handles printing and exit codes.
	RunE: runDiagnose,
}

func init() {
	// Register this command under rootCmd so `kubectl-ai diagnose` works.
	rootCmd.AddCommand(diagnoseCmd)
	diagnoseCmd.Flags().IntP("lines", "l", 50, "Number of log lines to fetch per container")
}

func runDiagnose(cmd *cobra.Command, args []string) error {
	// args[0] is the pod name — cobra already validated exactly one arg exists.
	podName := args[0]

	// The _ discards the error from GetString because these flags have defaults
	// and will never fail. For flags without defaults you'd check the error.
	namespace, _ := cmd.Flags().GetString("namespace")
	logLines, _ := cmd.Flags().GetInt("lines")
	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is not set\n\nRun: export ANTHROPIC_API_KEY=your-key-here")
	}

	// context.Background() is the root context — it never cancels.
	// We pass it through every function so they can respect cancellation later
	// (e.g. if the user hits Ctrl+C).
	ctx := context.Background()

	fmt.Printf("\n🔍 Fetching diagnostics for pod %q in namespace %q...\n", podName, namespace)

	client, err := k8s.NewClient(kubeconfig)
	if err != nil {
		return fmt.Errorf("could not connect to cluster: %w\n\nIs your kubeconfig set up? Try: kubectl get pods", err)
	}

	data, err := client.GatherDiagnostics(ctx, namespace, podName, logLines)
	if err != nil {
		return fmt.Errorf("failed to gather pod data: %w", err)
	}

	fmt.Printf("✅ Collected %d log lines, %d events, and pod spec\n", data.LogLineCount, data.EventCount)
	fmt.Println("🤖 Sending to Claude for analysis...\n")
	fmt.Println("─────────────────────────────────────────────")

	claudeClient := ai.NewClaudeClient(apiKey)
	diagnosis, err := claudeClient.Diagnose(ctx, data)
	if err != nil {
		return fmt.Errorf("AI diagnosis failed: %w", err)
	}

	fmt.Println(diagnosis)
	fmt.Println("─────────────────────────────────────────────")

	return nil
}
