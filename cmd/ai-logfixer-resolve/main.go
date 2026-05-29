package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CloudSpaceLab/ai-logfixer/internal/resolver"
)

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("ai-logfixer-resolve", flag.ContinueOnError)
	flags.SetOutput(stderr)

	targetDir := flags.String("target", ".", "application target directory")
	serviceName := flags.String("service", "", "service name to record in contracts")
	message := flags.String("message", "", "observed error message")
	stackTrace := flags.String("stack-trace", "", "inline runtime stack trace")
	stackTraceFile := flags.String("stack-trace-file", "", "file containing the runtime stack trace")
	apply := flags.Bool("apply", false, "apply sandbox-validated AI patch to the target")
	agentCommand := flags.String("agent-command", "", "external AI/coding agent command; placeholders: {prompt_file}, {staging_dir}, {target_dir}")
	agentModel := flags.String("agent-model", "", "optional external agent model")
	agentName := flags.String("agent", "", "optional external agent name")
	keepAgentWorkdir := flags.Bool("keep-agent-workdir", false, "keep the external agent staging workdir")
	maxAgentFiles := flags.Int("agent-max-files", 50, "maximum files an external agent may change")
	var validations stringList
	flags.Var(&validations, "validate", "validation command to run in the staging copy; repeatable")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	trace := *stackTrace
	if strings.TrimSpace(*stackTraceFile) != "" {
		raw, err := os.ReadFile(*stackTraceFile)
		if err != nil {
			fmt.Fprintf(stderr, "read stack trace file: %v\n", err)
			return 1
		}
		trace = string(raw)
	}

	result, err := resolver.Run(context.Background(), resolver.Options{
		ServiceName:        *serviceName,
		TargetDir:          *targetDir,
		StackTrace:         trace,
		Message:            *message,
		Apply:              *apply,
		ValidationCommands: validations,
		AgentCommand:       *agentCommand,
		AgentModel:         *agentModel,
		AgentName:          *agentName,
		KeepAgentWorkdir:   *keepAgentWorkdir,
		MaxChangedFiles:    *maxAgentFiles,
	})
	if err != nil {
		fmt.Fprintf(stderr, "run universal resolver: %v\n", err)
		return 1
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(stderr, "encode result: %v\n", err)
		return 1
	}

	fmt.Fprintln(stderr, "Universal resolver completed")
	return 0
}
