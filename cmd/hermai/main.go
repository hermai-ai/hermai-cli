package main

import (
	"errors"
	"os"

	appversion "github.com/hermai-ai/hermai-cli/internal/version"
	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/engine"
	"github.com/spf13/cobra"
)

// Set via ldflags at build time:
//
//	go build -ldflags "-X main.version=v0.1.0 -X main.commit=$(git rev-parse --short HEAD)"
var (
	version = "dev"
	commit  = "unknown"
)

// Exit codes for machine consumption.
const (
	exitOK            = 0
	exitGeneralError  = 1
	exitAuthRequired  = 2
	exitAnalysisFailed = 3
)

func newRootCmd() *cobra.Command {
	// Sync the internal version package so other packages (fetcher, probe) can use it
	appversion.Version = version

	root := &cobra.Command{
		Use:     "hermai",
		Short:   "Reverse-engineer website APIs and return structured JSON",
		Long: `Hermai is a CLI tool that discovers and documents website API endpoints
by observing browser network traffic and producing structured JSON schemas.`,
		Version:       version + " (" + commit + ")",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newInitCmd())
	root.AddCommand(newFetchCmd())
	root.AddCommand(newCacheCmd())
	root.AddCommand(newSchemaCmd())
	root.AddCommand(newDiscoverCmd())
	root.AddCommand(newCatalogCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newRegistryCmd())
	root.AddCommand(newSessionCmd())
	root.AddCommand(newProbeCmd())
	root.AddCommand(newExtractCmd())
	root.AddCommand(newWellKnownCmd())
	root.AddCommand(newIntrospectCmd())
	root.AddCommand(newReplayCmd())
	root.AddCommand(newDetectCmd())
	root.AddCommand(newInterceptCmd())
	root.AddCommand(newActionCmd())

	// Phase 2 commands — gated behind HERMAI_PHASE2=1 so the code stays
	// intact but doesn't clutter the Phase 1 CLI surface.
	if os.Getenv("HERMAI_PHASE2") == "1" {
		root.AddCommand(newExecuteCmd())
	}

	return root
}

func main() {
	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		os.Stderr.WriteString("Error: " + err.Error() + "\n")
		os.Exit(classifyError(err))
	}
}

// classifyError maps known error types to distinct exit codes
// so scripts and CI systems can react without parsing error messages.
func classifyError(err error) int {
	if errors.Is(err, browser.ErrAuthWall) || errors.Is(err, engine.ErrAuthRequired) {
		return exitAuthRequired
	}
	if errors.Is(err, engine.ErrAnalysisFailed) {
		return exitAnalysisFailed
	}
	return exitGeneralError
}
