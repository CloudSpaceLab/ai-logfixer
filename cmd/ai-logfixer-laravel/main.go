package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/CloudSpaceLab/ai-logfixer/internal/laravel"
)

type headerFlags map[string]string

func (h headerFlags) String() string {
	if len(h) == 0 {
		return ""
	}
	var parts []string
	for name, value := range h {
		parts = append(parts, name+": "+value)
	}
	return strings.Join(parts, ", ")
}

func (h headerFlags) Set(value string) error {
	name, headerValue, ok := strings.Cut(value, ":")
	if !ok {
		return fmt.Errorf("header must be in Name: value form")
	}
	name = strings.TrimSpace(name)
	headerValue = strings.TrimSpace(headerValue)
	if name == "" {
		return fmt.Errorf("header name is required")
	}
	h[name] = headerValue
	return nil
}

type stringListFlags []string

func (s *stringListFlags) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ", ")
}

func (s *stringListFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value is required")
	}
	*s = append(*s, value)
	return nil
}

func main() {
	headers := headerFlags{}
	validations := stringListFlags{}

	targetDir := flag.String("target", ".", "Laravel application target directory")
	serviceName := flag.String("service", "laravel-app", "service name to record in contracts")
	url := flag.String("url", "", "optional URL to probe before and after remediation")
	logPath := flag.String("log", "", "optional Laravel log path; defaults to latest storage/logs/laravel*.log")
	apply := flag.Bool("apply", true, "apply low-risk automatic remediation")
	statusOnly := flag.Bool("http-status-only", false, "treat only HTTP 5xx as an HTTP failure signal")
	externalAgent := flag.Bool("external-agent", false, "delegate unsupported Laravel issues to an external coding agent in a staging copy")
	agentCommand := flag.String("agent-command", "", "external coding agent command; defaults to opencode run with {prompt_file} attached")
	agentModel := flag.String("agent-model", "", "optional external agent model, e.g. opencode provider/model")
	agentName := flag.String("agent", "", "optional opencode agent name")
	keepAgentWorkdir := flag.Bool("keep-agent-workdir", false, "keep the external agent staging directory")
	maxAgentFiles := flag.Int("agent-max-files", 50, "maximum files an external agent may change")
	flag.Var(headers, "header", "HTTP header for URL probe; repeatable, e.g. -header 'Cookie: name=value'")
	flag.Var(&validations, "validate", "validation command to run in the external agent staging copy; repeatable")
	flag.Parse()

	result, err := laravel.Run(context.Background(), laravel.Options{
		ServiceName:           *serviceName,
		TargetDir:             *targetDir,
		URL:                   *url,
		LogPath:               *logPath,
		Apply:                 *apply,
		HTTPHeaders:           headers,
		HTTPStatusOnly:        *statusOnly,
		ExternalAgent:         *externalAgent,
		ExternalAgentCommand:  *agentCommand,
		ExternalAgentModel:    *agentModel,
		ExternalAgentName:     *agentName,
		ExternalAgentValidate: validations,
		ExternalAgentKeepWork: *keepAgentWorkdir,
		ExternalAgentMaxFiles: *maxAgentFiles,
	})
	if err != nil {
		log.Fatalf("run Laravel fixer: %v", err)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		log.Fatalf("encode result: %v", err)
	}

	if result.Attempt.Status == "escalated" {
		fmt.Fprintln(os.Stderr, "Laravel issue classified; automatic remediation escalated")
	} else if *apply {
		fmt.Fprintln(os.Stderr, "Laravel remediation completed")
	} else {
		fmt.Fprintln(os.Stderr, "Laravel dry run completed")
	}
}
