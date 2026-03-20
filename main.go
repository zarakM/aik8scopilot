package main

// In Go, every executable lives in "package main" and starts at func main().
// This file is intentionally tiny — it just hands off to the cmd package.
// This is idiomatic Go: main.go is a thin wrapper, real logic lives in packages.

import "kubectl-ai/cmd"

func main() {
	cmd.Execute()
}
