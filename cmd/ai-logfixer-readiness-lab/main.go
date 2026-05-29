package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/readiness"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("ai-logfixer-readiness-lab", flag.ContinueOnError)
	flags.SetOutput(stderr)

	manifestPath := flags.String("manifest", filepath.Join("labs", "readiness", "lab.json"), "readiness lab manifest")
	workDir := flags.String("workdir", "", "scratch directory for copied scenario apps")
	traceDir := flags.String("trace-dir", "", "optional directory of live trace/log overrides named <scenario-id>.log")
	agentCommand := flags.String("agent-command", "", "external AI/coding agent command; placeholders: {prompt_file}, {staging_dir}, {target_dir}")
	agentModel := flags.String("agent-model", "", "optional external agent model")
	agentName := flags.String("agent", "", "optional external agent name")
	concurrency := flags.Int("concurrency", 5, "number of readiness scenarios to run in parallel")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum readiness run duration")
	keepAgentWorkdir := flags.Bool("keep-agent-workdir", false, "keep each external agent staging workdir")
	inPlace := flags.Bool("in-place", false, "patch scenario app directories directly instead of copying them to a scratch directory")
	useDockerValidations := flags.Bool("use-docker-validations", false, "use docker_validation_commands from the manifest when available")
	maxFiles := flags.Int("agent-max-files", 50, "maximum files an external agent may change per scenario")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	cleanup := func() {}
	if *workDir == "" && !*inPlace {
		tempDir, err := os.MkdirTemp("", "ai-logfixer-readiness-*")
		if err != nil {
			fmt.Fprintf(stderr, "create readiness workdir: %v\n", err)
			return 1
		}
		*workDir = tempDir
		cleanup = func() { _ = os.RemoveAll(tempDir) }
	}
	defer cleanup()

	ctx := context.Background()
	cancel := func() {}
	if *timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, *timeout)
	}
	defer cancel()

	report, err := readiness.RunLocalSmoke(ctx, readiness.SmokeOptions{
		ManifestPath:        *manifestPath,
		WorkDir:             *workDir,
		TraceDir:            *traceDir,
		AgentCommand:        *agentCommand,
		AgentModel:          *agentModel,
		AgentName:           *agentName,
		Concurrency:         *concurrency,
		KeepWorkdir:         *keepAgentWorkdir,
		InPlace:             *inPlace,
		UseDockerValidation: *useDockerValidations,
		MaxFiles:            *maxFiles,
	})
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		fmt.Fprintf(stderr, "encode readiness report: %v\n", encodeErr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "readiness lab failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "Readiness lab passed %d/%d scenarios\n", report.Passed, report.Total)
	return 0
}
