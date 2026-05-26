package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	runtimev2 "github.com/CloudSpaceLab/ai-logfixer/internal/runtime/v2"
	"github.com/CloudSpaceLab/ai-logfixer/internal/truth"
)

type output struct {
	InvestigationRequest *contractsv1.InvestigationRequest `json:"investigation_request,omitempty"`
	Diagnosis            *contractsv1.DiagnosisResult      `json:"diagnosis,omitempty"`
	RemediationPlan      *contractsv1.RemediationPlan      `json:"remediation_plan,omitempty"`
	Attempt              *contractsv1.RemediationAttempt   `json:"attempt,omitempty"`
	Receipt              *contractsv1.Receipt              `json:"receipt,omitempty"`
	BackupPath           string                            `json:"backup_path,omitempty"`
	TruthRecovery        *truth.TruthRecoveryResult        `json:"truth_recovery,omitempty"`
}

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
	flags := flag.NewFlagSet("ai-logfixer-v2", flag.ContinueOnError)
	flags.SetOutput(stderr)

	mode := flags.String("mode", "config", "Runtime V2 mode: config or truth")

	serviceName := flags.String("service", "goravel-demo", "service name to investigate")
	baseURL := flags.String("base-url", "http://127.0.0.1:8090", "demo app base URL")
	configPath := flags.String("config", "./tmp/demo-goravel-app.json", "path to app JSON config")
	logPath := flags.String("log", "./tmp/demo-goravel-app.log", "path to app log")
	healthyUpstream := flags.String("healthy-upstream", "http://127.0.0.1:8090/upstream/orders", "healthy upstream URL to patch into config")
	method := flags.String("method", "", "HTTP method to match in logs when present")
	route := flags.String("route", "/orders", "HTTP route to match and verify by default")
	statusCode := flags.Int("status", 503, "HTTP status code to match")
	statusClass := flags.Int("status-class", 0, "HTTP status class to match when status is not set")
	configKey := flags.String("config-key", "upstream_url", "dot-separated JSON config key to patch")
	replacementValue := flags.String("replacement-value", "", "replacement string value for the config key; defaults to healthy-upstream")
	verifyURL := flags.String("verify-url", "", "URL to verify after patch; defaults to base-url + route")
	expectedStatus := flags.Int("expected-status", 200, "expected HTTP status from verify-url after patch")
	errorThreshold := flags.Int("threshold", 3, "minimum repeated failure count required to start remediation")

	framework := flags.String("framework", "go", "framework name for truth recovery")
	environment := flags.String("environment", string(truth.EnvironmentUnknown), "truth recovery environment: production, staging, local, or unknown")
	source := flags.String("source", "", "truth recovery source identifier, such as a log path or handler file")
	message := flags.String("message", "", "observed error message or custom log message")
	stackTraceFile := flags.String("stack-trace-file", "", "file containing a full stack trace")
	var sourceFiles stringList
	flags.Var(&sourceFiles, "source-file", "source file to inspect for suppression sites; repeatable")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	var result output
	var err error
	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "config":
		result, err = runConfigMode(runtimeConfigInput{
			serviceName:      *serviceName,
			baseURL:          *baseURL,
			configPath:       *configPath,
			logPath:          *logPath,
			healthyUpstream:  *healthyUpstream,
			method:           *method,
			route:            *route,
			statusCode:       *statusCode,
			statusClass:      *statusClass,
			configKey:        *configKey,
			replacementValue: *replacementValue,
			verifyURL:        *verifyURL,
			expectedStatus:   *expectedStatus,
			errorThreshold:   *errorThreshold,
		})
	case "truth":
		result, err = runTruthMode(truthInput{
			serviceName:    *serviceName,
			framework:      *framework,
			environment:    truth.Environment(*environment),
			source:         *source,
			message:        *message,
			stackTraceFile: *stackTraceFile,
			sourceFiles:    sourceFiles,
		})
	default:
		fmt.Fprintf(stderr, "unsupported Runtime V2 mode %q\n", *mode)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "run Runtime V2: %v\n", err)
		return 1
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(stderr, "encode result: %v\n", err)
		return 1
	}

	fmt.Fprintf(stderr, "Runtime V2 %s completed\n", strings.ToLower(strings.TrimSpace(*mode)))
	return 0
}

type runtimeConfigInput struct {
	serviceName      string
	baseURL          string
	configPath       string
	logPath          string
	healthyUpstream  string
	method           string
	route            string
	statusCode       int
	statusClass      int
	configKey        string
	replacementValue string
	verifyURL        string
	expectedStatus   int
	errorThreshold   int
}

func runConfigMode(input runtimeConfigInput) (output, error) {
	replacementValue := input.replacementValue
	if replacementValue == "" {
		replacementValue = input.healthyUpstream
	}
	result, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:      input.serviceName,
		BaseURL:          input.baseURL,
		LogPath:          input.logPath,
		ConfigPath:       input.configPath,
		HealthyUpstream:  input.healthyUpstream,
		ErrorThreshold:   input.errorThreshold,
		Method:           input.method,
		Route:            input.route,
		StatusCode:       input.statusCode,
		StatusClass:      input.statusClass,
		ConfigKeyPath:    input.configKey,
		ReplacementValue: replacementValue,
		VerifyURL:        input.verifyURL,
		ExpectedStatus:   input.expectedStatus,
	})
	if err != nil {
		return output{}, err
	}
	return output{
		InvestigationRequest: &result.InvestigationRequest,
		Diagnosis:            &result.Diagnosis,
		RemediationPlan:      &result.RemediationPlan,
		Attempt:              &result.Attempt,
		Receipt:              &result.Receipt,
		BackupPath:           result.BackupPath,
	}, nil
}

type truthInput struct {
	serviceName    string
	framework      string
	environment    truth.Environment
	source         string
	message        string
	stackTraceFile string
	sourceFiles    []string
}

func runTruthMode(input truthInput) (output, error) {
	stackTrace, err := readOptionalFile(input.stackTraceFile)
	if err != nil {
		return output{}, err
	}
	sourceFiles, err := readSourceFiles(input.sourceFiles)
	if err != nil {
		return output{}, err
	}
	result, err := truth.Recover(truth.RecoveryOptions{
		Signal: truth.ErrorSignal{
			Service:     input.serviceName,
			Framework:   input.framework,
			Source:      input.source,
			Message:     input.message,
			Environment: input.environment,
			StackTrace:  stackTrace,
		},
		SourceFiles: sourceFiles,
	})
	if err != nil {
		return output{}, err
	}
	out := output{TruthRecovery: &result}
	if !result.RevealPlan.Safe && len(result.RevealPlan.BlockedReasons) > 0 {
		reason := strings.Join(result.RevealPlan.BlockedReasons, "; ")
		plan, attempt, receipt, err := truth.BuildBlockedContracts(result.Signal, reason, result.Signal.ObservedAt)
		if err != nil {
			return output{}, err
		}
		out.RemediationPlan = &plan
		out.Attempt = &attempt
		out.Receipt = &receipt
	}
	return out, nil
}

func readOptionalFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(raw), nil
}

func readSourceFiles(paths []string) ([]truth.SourceFile, error) {
	var files []truth.SourceFile
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("read source file %s: %w", path, err)
		}
		files = append(files, truth.SourceFile{
			Path:    filepath.ToSlash(path),
			Content: string(raw),
		})
	}
	return files, nil
}
