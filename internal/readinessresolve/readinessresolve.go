package readinessresolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	runtimev2 "github.com/CloudSpaceLab/ai-logfixer/internal/runtime/v2"
)

const SchemaVersion = "ai-logfixer-readiness-resolver/v1"

type Status string

const (
	StatusResolved    Status = "resolved"
	StatusUnsupported Status = "unsupported"
	StatusInvalid     Status = "invalid"
	StatusFailed      Status = "failed"
)

type CandidateInput struct {
	ScenarioID          string `json:"scenario_id"`
	OperationalLane     string `json:"operational_lane"`
	Runtime             string `json:"runtime"`
	AppCarrier          string `json:"app_carrier"`
	ServiceName         string `json:"service_name"`
	DockerService       string `json:"docker_service"`
	AppDir              string `json:"app_dir"`
	PolicyFile          string `json:"policy_file"`
	TraceFile           string `json:"trace_file"`
	InventoryDir        string `json:"inventory_dir"`
	ComposeFile         string `json:"compose_file"`
	ComposeProject      string `json:"compose_project"`
	LiveProbeURL        string `json:"live_probe_url"`
	ExpectedFixedStatus int    `json:"expected_fixed_status"`
	FixedBodyContains   string `json:"fixed_body_contains"`
	SafeAction          string `json:"safe_action"`
}

type Response struct {
	SchemaVersion   string                          `json:"schema_version"`
	ScenarioID      string                          `json:"scenario_id,omitempty"`
	OperationalLane string                          `json:"operational_lane,omitempty"`
	Supported       bool                            `json:"supported"`
	Status          Status                          `json:"status"`
	Message         string                          `json:"message"`
	RemediationPlan *contractsv1.RemediationPlan    `json:"remediation_plan,omitempty"`
	Attempt         *contractsv1.RemediationAttempt `json:"attempt,omitempty"`
	Receipt         *contractsv1.Receipt            `json:"receipt,omitempty"`
	BackupPath      string                          `json:"backup_path,omitempty"`
}

func LoadCandidateInput(path string) (CandidateInput, error) {
	if strings.TrimSpace(path) == "" {
		return CandidateInput{}, errors.New("input path is required")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return CandidateInput{}, fmt.Errorf("read candidate input: %w", err)
	}
	var input CandidateInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return CandidateInput{}, fmt.Errorf("decode candidate input: %w", err)
	}
	if err := input.Validate(); err != nil {
		return CandidateInput{}, err
	}
	return input, nil
}

func (input CandidateInput) Validate() error {
	required := map[string]string{
		"scenario_id":      input.ScenarioID,
		"operational_lane": input.OperationalLane,
		"service_name":     input.ServiceName,
		"app_dir":          input.AppDir,
		"policy_file":      input.PolicyFile,
		"trace_file":       input.TraceFile,
		"live_probe_url":   input.LiveProbeURL,
	}
	var missing []string
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, field+" is required")
		}
	}
	if len(missing) > 0 {
		return errors.New(strings.Join(missing, "; "))
	}
	return nil
}

func Resolve(ctx context.Context, input CandidateInput) (Response, error) {
	if err := input.Validate(); err != nil {
		return Response{}, err
	}

	switch normalizeLane(input.OperationalLane) {
	case "config-drift":
		return resolveConfigDrift(ctx, input)
	default:
		return unsupportedResponse(input), nil
	}
}

func FailedResponse(input CandidateInput, message string) Response {
	return baseResponse(input, StatusFailed, false, message)
}

func InvalidResponse(message string) Response {
	return Response{
		SchemaVersion: SchemaVersion,
		Status:        StatusInvalid,
		Message:       message,
	}
}

func unsupportedResponse(input CandidateInput) Response {
	return baseResponse(
		input,
		StatusUnsupported,
		false,
		fmt.Sprintf("readiness lane %q is not implemented on main; no remediation was attempted", input.OperationalLane),
	)
}

func baseResponse(input CandidateInput, status Status, supported bool, message string) Response {
	return Response{
		SchemaVersion:   SchemaVersion,
		ScenarioID:      input.ScenarioID,
		OperationalLane: input.OperationalLane,
		Supported:       supported,
		Status:          status,
		Message:         message,
	}
}

type configPolicy struct {
	Lane           string             `json:"lane"`
	AllowedFiles   []string           `json:"allowed_files"`
	AllowedKeys    []string           `json:"allowed_keys"`
	TrustedSources []string           `json:"trusted_sources"`
	Verification   policyVerification `json:"verification"`
}

type policyVerification struct {
	Method         string `json:"method"`
	URL            string `json:"url"`
	ExpectedStatus int    `json:"expected_status"`
	BodyContains   string `json:"body_contains"`
}

func resolveConfigDrift(ctx context.Context, input CandidateInput) (Response, error) {
	policy, err := loadConfigPolicy(input.PolicyFile)
	if err != nil {
		return Response{}, err
	}
	descriptor, err := configPatchDescriptor(input.AppDir, policy)
	if err != nil {
		return Response{}, err
	}
	replacementValue, err := readJSONString(descriptor.trustedPath, descriptor.keyPath)
	if err != nil {
		return Response{}, err
	}
	verifyURL := firstNonEmpty(input.LiveProbeURL, policy.Verification.URL)
	expectedStatus := input.ExpectedFixedStatus
	if expectedStatus == 0 {
		expectedStatus = policy.Verification.ExpectedStatus
	}
	if expectedStatus == 0 {
		expectedStatus = http.StatusOK
	}
	route, err := routeFromURL(verifyURL)
	if err != nil {
		return Response{}, err
	}

	result, err := runtimev2.Run(ctx, runtimev2.Options{
		ServiceName:      input.ServiceName,
		LogPath:          input.TraceFile,
		ConfigPath:       descriptor.configPath,
		Method:           http.MethodGet,
		Route:            route,
		StatusClass:      http.StatusInternalServerError,
		ConfigKeyPath:    descriptor.keyPath,
		ReplacementValue: replacementValue,
		VerifyURL:        verifyURL,
		ExpectedStatus:   expectedStatus,
		ErrorThreshold:   1,
		AfterConfigPatch: dockerConfigSyncHook(input, descriptor.configRelativePath),
	})
	if err != nil {
		return Response{}, err
	}

	response := baseResponse(input, StatusResolved, true, "config-drift resolver completed")
	response.RemediationPlan = &result.RemediationPlan
	response.Attempt = &result.Attempt
	response.Receipt = &result.Receipt
	response.BackupPath = result.BackupPath
	return response, nil
}

func loadConfigPolicy(path string) (configPolicy, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return configPolicy{}, fmt.Errorf("read config-drift policy: %w", err)
	}
	var policy configPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return configPolicy{}, fmt.Errorf("decode config-drift policy: %w", err)
	}
	if normalizeLane(policy.Lane) != "config-drift" {
		return configPolicy{}, fmt.Errorf("policy lane %q does not match config-drift", policy.Lane)
	}
	if len(policy.AllowedFiles) == 0 {
		return configPolicy{}, errors.New("config-drift policy allowed_files is required")
	}
	if len(policy.AllowedKeys) == 0 {
		return configPolicy{}, errors.New("config-drift policy allowed_keys is required")
	}
	if len(policy.TrustedSources) == 0 {
		return configPolicy{}, errors.New("config-drift policy trusted_sources is required")
	}
	return policy, nil
}

type configDescriptor struct {
	configPath         string
	configRelativePath string
	keyPath            string
	trustedPath        string
}

func configPatchDescriptor(appDir string, policy configPolicy) (configDescriptor, error) {
	configPath, err := joinInsideApp(appDir, policy.AllowedFiles[0])
	if err != nil {
		return configDescriptor{}, fmt.Errorf("resolve allowed config file: %w", err)
	}
	trustedPath, err := joinInsideApp(appDir, policy.TrustedSources[0])
	if err != nil {
		return configDescriptor{}, fmt.Errorf("resolve trusted config source: %w", err)
	}
	return configDescriptor{
		configPath:         configPath,
		configRelativePath: filepath.Clean(policy.AllowedFiles[0]),
		keyPath:            strings.TrimSpace(policy.AllowedKeys[0]),
		trustedPath:        trustedPath,
	}, nil
}

func joinInsideApp(appDir string, relativePath string) (string, error) {
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("%s must be relative to app_dir", relativePath)
	}
	appAbs, err := filepath.Abs(filepath.Clean(appDir))
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(appAbs, filepath.Clean(relativePath))
	rel, err := filepath.Rel(appAbs, candidate)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s escapes app_dir", relativePath)
	}
	return candidate, nil
}

func readJSONString(path string, keyPath string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read trusted source: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", fmt.Errorf("decode trusted source: %w", err)
	}
	current := any(document)
	for _, segment := range strings.Split(keyPath, ".") {
		segment = strings.TrimSpace(segment)
		object, ok := current.(map[string]any)
		if !ok {
			return "", fmt.Errorf("trusted source key %q is not a string", keyPath)
		}
		current = object[segment]
	}
	value, ok := current.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("trusted source key %q is not a non-empty string", keyPath)
	}
	return value, nil
}

func routeFromURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse live_probe_url: %w", err)
	}
	if parsed.Path == "" {
		return "/", nil
	}
	return parsed.Path, nil
}

func normalizeLane(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func dockerConfigSyncHook(input CandidateInput, relativePath string) runtimev2.ConfigPatchHook {
	if strings.TrimSpace(input.ComposeFile) == "" ||
		strings.TrimSpace(input.ComposeProject) == "" ||
		strings.TrimSpace(input.DockerService) == "" {
		return nil
	}
	containerPath := "/app/" + filepath.ToSlash(filepath.Clean(relativePath))
	return func(ctx context.Context, patch runtimev2.ConfigPatch) error {
		target := input.DockerService + ":" + containerPath
		args := []string{
			"compose",
			"-f", input.ComposeFile,
			"-p", input.ComposeProject,
			"cp",
			patch.ConfigPath,
			target,
		}
		command := exec.CommandContext(ctx, "docker", args...)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
		return nil
	}
}
