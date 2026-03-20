package cmd

// Cobra is the same library kubectl itself uses for its CLI.
// Every command (diagnose, explain, etc.) gets registered to rootCmd.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd is the base command. Running `kubectl-ai` alone prints help.
var rootCmd = &cobra.Command{
	Use:   "kubectl-ai",
	Short: "AI-powered Kubernetes diagnostics",
	Long: `kubectl-ai is an AI-native SRE that diagnoses Kubernetes issues in real time.

It collects pod logs, events, and cluster state, then uses an LLM to
explain what's wrong and what to do about it — in plain English.

Examples:
  kubectl ai diagnose my-pod
  kubectl ai diagnose my-pod -n production
  kubectl ai diagnose my-pod -n production --lines 100`,
}

// Execute is called by main.go. It runs the CLI and handles any top-level error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// PersistentFlags are inherited by every sub-command (diagnose, explain, etc.)
	rootCmd.PersistentFlags().StringP("namespace", "n", "default", "Kubernetes namespace")
	rootCmd.PersistentFlags().StringP("kubeconfig", "", "", "Path to kubeconfig (defaults to ~/.kube/config)")
}
