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
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/runtime/databases"
	envvars "github.com/CloudSpaceLab/ai-logfixer/internal/runtime/envvars"
	permissions "github.com/CloudSpaceLab/ai-logfixer/internal/runtime/permissions"
	"github.com/CloudSpaceLab/ai-logfixer/internal/runtime/resources"
	"github.com/CloudSpaceLab/ai-logfixer/internal/runtime/restart"
	"github.com/CloudSpaceLab/ai-logfixer/internal/runtime/tokens"
	runtimev2 "github.com/CloudSpaceLab/ai-logfixer/internal/runtime/v2"
	"github.com/CloudSpaceLab/ai-logfixer/internal/runtime/versions"
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
	Framework            string                            `json:"framework,omitempty"`
	PermissionPolicy     *permissions.PermissionPolicy     `json:"permission_policy,omitempty"`
	PermissionFindings   []permissions.PermissionFinding   `json:"permission_findings,omitempty"`
	PermissionOperations []permissions.PermissionOperation `json:"permission_operations,omitempty"`
	RollbackPath         string                            `json:"rollback_path,omitempty"`
	EnvVars              *envvars.Result                   `json:"envvars,omitempty"`
	Database             *databases.Result                 `json:"database,omitempty"`
	Resource             *resources.Result                 `json:"resource,omitempty"`
	Restart              *restartModeOutput                `json:"restart,omitempty"`
	Token                *tokens.Result                    `json:"token,omitempty"`
	Versions             *versions.Result                  `json:"versions,omitempty"`
}

type restartModeOutput struct {
	InvestigationRequest contractsv1.InvestigationRequest `json:"investigation_request"`
	Diagnosis            contractsv1.DiagnosisResult      `json:"diagnosis"`
	RemediationPlan      contractsv1.RemediationPlan      `json:"remediation_plan"`
	Attempt              contractsv1.RemediationAttempt   `json:"attempt"`
	Receipt              contractsv1.Receipt              `json:"receipt"`
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

	mode := flags.String("mode", "config", "Runtime V2 mode: config, permissions, truth, envvars, database, resources, restart, tokens, or versions")
	inputPath := flags.String("input", "", "path to JSON input for Runtime V2 diagnostic/remediation modes")

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
	targetDir := flags.String("target", "", "target app directory for filesystem remediation modes")
	apply := flags.Bool("apply", false, "apply filesystem remediation instead of dry-run planning")

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
	case "permissions":
		result, err = runPermissionsMode(permissionInput{
			serviceName:    *serviceName,
			targetDir:      *targetDir,
			framework:      *framework,
			verifyURL:      *verifyURL,
			expectedStatus: *expectedStatus,
			apply:          *apply,
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
	case "envvars":
		result, err = runEnvvarsMode(*inputPath)
	case "database":
		result, err = runDatabaseMode(*inputPath)
	case "resources":
		result, err = runResourcesMode(*inputPath)
	case "restart":
		result, err = runRestartMode(*inputPath)
	case "tokens":
		result, err = runTokensMode(*inputPath)
	case "versions":
		result, err = runVersionsMode(*inputPath)
	default:
		fmt.Fprintf(stderr, "unsupported Runtime V2 mode %q\n", *mode)
		return 2
	}
	if err != nil {
		if hasStructuredOutput(result) {
			if encodeErr := encodeOutput(stdout, result); encodeErr != nil {
				fmt.Fprintf(stderr, "encode result: %v\n", encodeErr)
				return 1
			}
		}
		fmt.Fprintf(stderr, "run Runtime V2: %v\n", err)
		return 1
	}

	if err := encodeOutput(stdout, result); err != nil {
		fmt.Fprintf(stderr, "encode result: %v\n", err)
		return 1
	}

	fmt.Fprintf(stderr, "Runtime V2 %s completed\n", strings.ToLower(strings.TrimSpace(*mode)))
	return 0
}

func encodeOutput(stdout io.Writer, result output) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func hasStructuredOutput(result output) bool {
	return result.InvestigationRequest != nil ||
		result.Diagnosis != nil ||
		result.RemediationPlan != nil ||
		result.Attempt != nil ||
		result.Receipt != nil ||
		result.BackupPath != "" ||
		result.TruthRecovery != nil ||
		result.EnvVars != nil ||
		result.Database != nil ||
		result.Resource != nil ||
		result.Restart != nil ||
		result.Token != nil ||
		result.Versions != nil
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
	out := output{
		InvestigationRequest: &result.InvestigationRequest,
		Diagnosis:            &result.Diagnosis,
		RemediationPlan:      &result.RemediationPlan,
		Attempt:              &result.Attempt,
		Receipt:              &result.Receipt,
		BackupPath:           result.BackupPath,
	}
	if err != nil {
		if !hasRuntimeV2Result(result) {
			return output{}, err
		}
		return out, err
	}
	return out, nil
}

func hasRuntimeV2Result(result runtimev2.Result) bool {
	return result.InvestigationRequest.ID != "" ||
		result.Diagnosis.ID != "" ||
		result.RemediationPlan.ID != "" ||
		result.Attempt.ID != "" ||
		result.Receipt.ID != "" ||
		result.BackupPath != ""
}

type permissionInput struct {
	serviceName    string
	targetDir      string
	framework      string
	verifyURL      string
	expectedStatus int
	apply          bool
}

func runPermissionsMode(input permissionInput) (output, error) {
	result, err := permissions.Run(context.Background(), permissions.Options{
		ServiceName:    input.serviceName,
		TargetDir:      input.targetDir,
		Framework:      input.framework,
		VerifyURL:      input.verifyURL,
		ExpectedStatus: input.expectedStatus,
		Apply:          input.apply,
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
		Framework:            result.Framework,
		PermissionPolicy:     &result.Policy,
		PermissionFindings:   result.Findings,
		PermissionOperations: result.Operations,
		RollbackPath:         result.RollbackPath,
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
	publicResult := truth.PublicResult(result)
	out := output{TruthRecovery: &publicResult}
	if !publicResult.RevealPlan.Safe && len(publicResult.RevealPlan.BlockedReasons) > 0 {
		reason := strings.Join(publicResult.RevealPlan.BlockedReasons, "; ")
		plan, attempt, receipt, err := truth.BuildBlockedContracts(publicResult.Signal, reason, publicResult.Signal.ObservedAt)
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

type envvarsInput struct {
	ServiceName string            `json:"service_name"`
	EnvFilePath string            `json:"env_file_path"`
	Policy      envvars.Policy    `json:"policy"`
	Environment map[string]string `json:"environment"`
	Apply       bool              `json:"apply"`
}

func runEnvvarsMode(inputPath string) (output, error) {
	var input envvarsInput
	if err := decodeInputFile(inputPath, &input); err != nil {
		return output{}, err
	}
	lookup := os.LookupEnv
	if input.Environment != nil {
		lookup = func(name string) (string, bool) {
			value, ok := input.Environment[name]
			return value, ok
		}
	}
	result, err := envvars.Run(context.Background(), envvars.Options{
		ServiceName: input.ServiceName,
		EnvFilePath: input.EnvFilePath,
		Policy:      input.Policy,
		LookupEnv:   lookup,
		Apply:       input.Apply,
	})
	if err != nil {
		return output{}, err
	}
	return output{EnvVars: &result}, nil
}

type databaseInput struct {
	ServiceName       string                       `json:"service_name"`
	DatabaseURL       string                       `json:"database_url"`
	AllowedSchemes    []string                     `json:"allowed_schemes"`
	AllowedHosts      []string                     `json:"allowed_hosts"`
	RequiredDatabase  string                       `json:"required_database"`
	RequiredTables    []databases.TableExpectation `json:"required_tables"`
	ObservedTables    []databases.TableState       `json:"observed_tables"`
	ConnectionProbe   databases.ProbeResult        `json:"connection_probe"`
	AllowAutoMutation bool                         `json:"allow_auto_mutation"`
}

func runDatabaseMode(inputPath string) (output, error) {
	var input databaseInput
	if err := decodeInputFile(inputPath, &input); err != nil {
		return output{}, err
	}
	result, err := databases.Diagnose(databases.Options{
		ServiceName:       input.ServiceName,
		DatabaseURL:       input.DatabaseURL,
		AllowedSchemes:    input.AllowedSchemes,
		AllowedHosts:      input.AllowedHosts,
		RequiredDatabase:  input.RequiredDatabase,
		RequiredTables:    input.RequiredTables,
		ObservedTables:    input.ObservedTables,
		ConnectionProbe:   input.ConnectionProbe,
		AllowAutoMutation: input.AllowAutoMutation,
	})
	if err != nil {
		return output{}, err
	}
	return output{Database: &result}, nil
}

type resourcesInput struct {
	AppRoot          string                    `json:"app_root"`
	ResourcePath     string                    `json:"resource_path"`
	Allowlist        []string                  `json:"allowlist"`
	Kind             resources.Kind            `json:"kind"`
	Mode             os.FileMode               `json:"mode"`
	ContentStrategy  resources.ContentStrategy `json:"content_strategy"`
	Content          string                    `json:"content"`
	VerifyURL        string                    `json:"verify_url"`
	ExpectedStatus   int                       `json:"expected_status"`
	VerifyCommand    string                    `json:"verify_command"`
	ManifestBaseName string                    `json:"manifest_base_name"`
}

func runResourcesMode(inputPath string) (output, error) {
	var input resourcesInput
	if err := decodeInputFile(inputPath, &input); err != nil {
		return output{}, err
	}
	result, err := resources.Ensure(context.Background(), resources.Options{
		AppRoot:          input.AppRoot,
		ResourcePath:     input.ResourcePath,
		Allowlist:        input.Allowlist,
		Kind:             input.Kind,
		Mode:             input.Mode,
		ContentStrategy:  input.ContentStrategy,
		Content:          input.Content,
		VerifyURL:        input.VerifyURL,
		ExpectedStatus:   input.ExpectedStatus,
		VerifyCommand:    input.VerifyCommand,
		ManifestBaseName: input.ManifestBaseName,
	})
	if err != nil {
		return output{Resource: &result}, err
	}
	return output{Resource: &result}, nil
}

type restartInput struct {
	ServiceName    string             `json:"service_name"`
	LogPath        string             `json:"log_path"`
	Method         string             `json:"method"`
	Route          string             `json:"route"`
	StatusCode     int                `json:"status_code"`
	StatusClass    int                `json:"status_class"`
	ErrorThreshold int                `json:"error_threshold"`
	Window         time.Duration      `json:"window"`
	ActionName     string             `json:"action_name"`
	Policy         restartPolicyInput `json:"policy"`
	Verification   restartVerifyInput `json:"verification"`
}

type restartPolicyInput struct {
	AllowedActions []restartActionInput `json:"allowed_actions"`
}

type restartActionInput struct {
	Name        string              `json:"name"`
	ServiceName string              `json:"service_name"`
	Command     restartCommandInput `json:"command"`
}

type restartCommandInput struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
}

type restartVerifyInput struct {
	HTTP    *restartHTTPVerifyInput    `json:"http"`
	Command *restartCommandVerifyInput `json:"command"`
}

type restartHTTPVerifyInput struct {
	URL            string `json:"url"`
	ExpectedStatus int    `json:"expected_status"`
	BodyContains   string `json:"body_contains"`
}

type restartCommandVerifyInput struct {
	Command        restartCommandInput `json:"command"`
	OutputContains string              `json:"output_contains"`
}

func runRestartMode(inputPath string) (output, error) {
	var input restartInput
	if err := decodeInputFile(inputPath, &input); err != nil {
		return output{}, err
	}
	result, err := restart.Run(context.Background(), restart.Options{
		ServiceName:    input.ServiceName,
		LogPath:        input.LogPath,
		Method:         input.Method,
		Route:          input.Route,
		StatusCode:     input.StatusCode,
		StatusClass:    input.StatusClass,
		ErrorThreshold: input.ErrorThreshold,
		Window:         input.Window,
		ActionName:     input.ActionName,
		Policy:         input.Policy.toPolicy(),
		Verification:   input.Verification.toVerification(),
	})
	restartOut := restartModeOutput{
		InvestigationRequest: result.InvestigationRequest,
		Diagnosis:            result.Diagnosis,
		RemediationPlan:      result.RemediationPlan,
		Attempt:              result.Attempt,
		Receipt:              result.Receipt,
	}
	if err != nil {
		return output{Restart: &restartOut}, err
	}
	return output{Restart: &restartOut}, nil
}

func (input restartPolicyInput) toPolicy() restart.Policy {
	actions := make([]restart.Action, 0, len(input.AllowedActions))
	for _, action := range input.AllowedActions {
		actions = append(actions, restart.Action{
			Name:        action.Name,
			ServiceName: action.ServiceName,
			Command:     action.Command.toCommand(),
		})
	}
	return restart.Policy{AllowedActions: actions}
}

func (input restartVerifyInput) toVerification() restart.Verification {
	var verification restart.Verification
	if input.HTTP != nil {
		verification.HTTP = &restart.HTTPVerification{
			URL:            input.HTTP.URL,
			ExpectedStatus: input.HTTP.ExpectedStatus,
			BodyContains:   input.HTTP.BodyContains,
		}
	}
	if input.Command != nil {
		verification.Command = &restart.CommandVerification{
			Command:        input.Command.Command.toCommand(),
			OutputContains: input.Command.OutputContains,
		}
	}
	return verification
}

func (input restartCommandInput) toCommand() restart.Command {
	return restart.Command{
		Path: input.Path,
		Args: append([]string(nil), input.Args...),
		Dir:  input.Dir,
		Env:  append([]string(nil), input.Env...),
	}
}

type tokensInput struct {
	ServiceName       string       `json:"service_name"`
	Provider          string       `json:"provider"`
	TokenName         string       `json:"token_name"`
	TokenPresent      bool         `json:"token_present"`
	TokenValue        string       `json:"token_value"`
	Probe             tokens.Probe `json:"probe"`
	RequiredScopes    []string     `json:"required_scopes"`
	ObservedScopes    []string     `json:"observed_scopes"`
	ExpiresAt         time.Time    `json:"expires_at"`
	AllowAutoMutation bool         `json:"allow_auto_mutation"`
}

func runTokensMode(inputPath string) (output, error) {
	var input tokensInput
	if err := decodeInputFile(inputPath, &input); err != nil {
		return output{}, err
	}
	result, err := tokens.Diagnose(tokens.Options{
		ServiceName:       input.ServiceName,
		Provider:          input.Provider,
		TokenName:         input.TokenName,
		TokenPresent:      input.TokenPresent,
		TokenValue:        input.TokenValue,
		Probe:             input.Probe,
		RequiredScopes:    input.RequiredScopes,
		ObservedScopes:    input.ObservedScopes,
		ExpiresAt:         input.ExpiresAt,
		AllowAutoMutation: input.AllowAutoMutation,
	})
	if err != nil {
		return output{}, err
	}
	return output{Token: &result}, nil
}

type versionsInput struct {
	ServiceName string                     `json:"service_name"`
	Required    []versions.Requirement     `json:"required"`
	Observed    []versions.ObservedVersion `json:"observed"`
	AllowRepair bool                       `json:"allow_repair"`
}

func runVersionsMode(inputPath string) (output, error) {
	var input versionsInput
	if err := decodeInputFile(inputPath, &input); err != nil {
		return output{}, err
	}
	result, err := versions.Diagnose(versions.Options{
		ServiceName: input.ServiceName,
		Required:    input.Required,
		Observed:    input.Observed,
		AllowRepair: input.AllowRepair,
	})
	if err != nil {
		return output{}, err
	}
	return output{Versions: &result}, nil
}

func decodeInputFile(path string, target any) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("-input is required for this Runtime V2 mode")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read input file: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode input file: %w", err)
	}
	return nil
}
