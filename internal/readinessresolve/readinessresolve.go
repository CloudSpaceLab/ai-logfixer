package readinessresolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	packagerollback "github.com/CloudSpaceLab/ai-logfixer/internal/resolvers/packages"
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
	case "package-regression":
		return resolvePackageRegression(ctx, input)
	case "permission-drift":
		return resolvePermissionDrift(ctx, input)
	case "restart-reload":
		return resolveRestartReload(ctx, input)
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

type packagePolicy struct {
	Lane            string             `json:"lane"`
	AllowedFiles    []string           `json:"allowed_files"`
	AllowedPackages []string           `json:"allowed_packages"`
	TrustedSources  []string           `json:"trusted_sources"`
	Verification    policyVerification `json:"verification"`
}

type packageHistory struct {
	Package       string `json:"package"`
	LastKnownGood string `json:"last_known_good"`
	Current       string `json:"current"`
}

type permissionPolicy struct {
	Lane              string             `json:"lane"`
	AllowedPaths      []string           `json:"allowed_paths"`
	PermissionTargets []permissionTarget `json:"permission_targets"`
	ExpectedOwner     string             `json:"expected_owner"`
	ExpectedGroup     string             `json:"expected_group"`
	ExpectedMode      string             `json:"expected_mode"`
	Verification      policyVerification `json:"verification"`
	Targets           []permissionTarget `json:"-"`
}

type permissionTarget struct {
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Access        string `json:"access"`
	ExpectedOwner string `json:"expected_owner"`
	ExpectedGroup string `json:"expected_group"`
	ExpectedMode  string `json:"expected_mode"`
}

type restartPolicy struct {
	Lane                  string             `json:"lane"`
	AllowedRestartTargets []string           `json:"allowed_restart_targets"`
	Verification          policyVerification `json:"verification"`
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

func resolvePackageRegression(ctx context.Context, input CandidateInput) (Response, error) {
	policy, err := loadPackagePolicy(input.PolicyFile)
	if err != nil {
		return Response{}, err
	}
	packageFile, err := joinInsideApp(input.AppDir, policy.AllowedFiles[0])
	if err != nil {
		return Response{}, fmt.Errorf("resolve package file: %w", err)
	}
	historyPath, err := joinInsideApp(input.AppDir, policy.TrustedSources[0])
	if err != nil {
		return Response{}, fmt.Errorf("resolve package history: %w", err)
	}
	history, err := loadPackageHistory(historyPath)
	if err != nil {
		return Response{}, err
	}
	if !containsString(policy.AllowedPackages, history.Package) {
		return Response{}, fmt.Errorf("package %q is not allowlisted by policy", history.Package)
	}

	verifyURL := firstNonEmpty(input.LiveProbeURL, policy.Verification.URL)
	expectedStatus := firstNonZero(input.ExpectedFixedStatus, policy.Verification.ExpectedStatus, http.StatusOK)
	options := packagerollback.Options{
		PackageFile:    packageFile,
		PackageName:    history.Package,
		CurrentSpec:    history.Current,
		KnownGoodSpec:  history.LastKnownGood,
		VerifyURL:      verifyURL,
		ExpectedStatus: expectedStatus,
		WorkingDir:     input.AppDir,
	}
	if hasDockerTarget(input) {
		options.VerifyURL = ""
		options.VerifyCommand = dockerPackageVerifyCommand(input, packageFile, verifyURL, expectedStatus, firstNonEmpty(input.FixedBodyContains, policy.Verification.BodyContains))
	}
	result, err := packagerollback.Rollback(ctx, options)
	if err != nil {
		return Response{}, err
	}
	response := baseResponse(input, StatusResolved, true, "package-regression resolver completed")
	response.BackupPath = result.Rollback.BackupPath
	return response, nil
}

func resolvePermissionDrift(ctx context.Context, input CandidateInput) (Response, error) {
	policy, err := loadPermissionPolicy(input.PolicyFile)
	if err != nil {
		return Response{}, err
	}
	for _, target := range policy.Targets {
		relativePath := target.Path
		mode, err := parsePermissionMode(target.ExpectedMode)
		if err != nil {
			return Response{}, err
		}
		if _, err := joinInsideApp(input.AppDir, relativePath); err != nil {
			return Response{}, fmt.Errorf("resolve allowlisted permission path: %w", err)
		}
		if hasDockerTarget(input) {
			if err := dockerRepairPermissions(ctx, input, target, policy); err != nil {
				return Response{}, err
			}
			continue
		}
		path, _ := joinInsideApp(input.AppDir, relativePath)
		switch target.Kind {
		case "dir":
			if err := os.MkdirAll(path, mode); err != nil {
				return Response{}, fmt.Errorf("mkdir %s: %w", relativePath, err)
			}
			if err := os.Chmod(path, mode); err != nil {
				return Response{}, fmt.Errorf("chmod %s: %w", relativePath, err)
			}
		case "file":
			if err := repairFileParentSearchPermission(input.AppDir, target, policy); err != nil {
				return Response{}, err
			}
			info, err := os.Stat(path)
			if os.IsNotExist(err) && target.Access == "write" {
				file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
				if createErr != nil {
					return Response{}, fmt.Errorf("create writable file target %s: %w", relativePath, createErr)
				}
				if closeErr := file.Close(); closeErr != nil {
					return Response{}, fmt.Errorf("close writable file target %s: %w", relativePath, closeErr)
				}
				info, err = os.Stat(path)
			}
			if err != nil {
				return Response{}, fmt.Errorf("stat %s: %w", relativePath, err)
			}
			if info.IsDir() {
				return Response{}, fmt.Errorf("permission target %s is a directory, expected file", relativePath)
			}
			if err := os.Chmod(path, mode); err != nil {
				return Response{}, fmt.Errorf("chmod %s: %w", relativePath, err)
			}
		default:
			return Response{}, fmt.Errorf("permission target %s has unsupported kind %q", relativePath, target.Kind)
		}
	}
	if err := verifyLiveProbe(ctx, input, policy.Verification); err != nil {
		return Response{}, err
	}
	return baseResponse(input, StatusResolved, true, "permission-drift resolver completed"), nil
}

func resolveRestartReload(ctx context.Context, input CandidateInput) (Response, error) {
	policy, err := loadRestartPolicy(input.PolicyFile)
	if err != nil {
		return Response{}, err
	}
	if !containsString(policy.AllowedRestartTargets, input.DockerService) {
		return Response{}, fmt.Errorf("restart target %q is not allowlisted by policy", input.DockerService)
	}
	if !hasDockerTarget(input) {
		return Response{}, errors.New("restart-reload readiness remediation requires compose file, project, and docker service")
	}
	args := []string{"compose", "-f", input.ComposeFile, "-p", input.ComposeProject, "restart", input.DockerService}
	command := exec.CommandContext(ctx, "docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return Response{}, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	if err := verifyLiveProbe(ctx, input, policy.Verification); err != nil {
		return Response{}, err
	}
	return baseResponse(input, StatusResolved, true, "restart-reload resolver completed"), nil
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

func loadPackagePolicy(path string) (packagePolicy, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return packagePolicy{}, fmt.Errorf("read package-regression policy: %w", err)
	}
	var policy packagePolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return packagePolicy{}, fmt.Errorf("decode package-regression policy: %w", err)
	}
	if normalizeLane(policy.Lane) != "package-regression" {
		return packagePolicy{}, fmt.Errorf("policy lane %q does not match package-regression", policy.Lane)
	}
	if len(policy.AllowedFiles) == 0 {
		return packagePolicy{}, errors.New("package-regression policy allowed_files is required")
	}
	if len(policy.AllowedPackages) == 0 {
		return packagePolicy{}, errors.New("package-regression policy allowed_packages is required")
	}
	if len(policy.TrustedSources) == 0 {
		return packagePolicy{}, errors.New("package-regression policy trusted_sources is required")
	}
	return policy, nil
}

func loadPackageHistory(path string) (packageHistory, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return packageHistory{}, fmt.Errorf("read package history: %w", err)
	}
	var history packageHistory
	if err := json.Unmarshal(raw, &history); err != nil {
		return packageHistory{}, fmt.Errorf("decode package history: %w", err)
	}
	if strings.TrimSpace(history.Package) == "" || strings.TrimSpace(history.Current) == "" || strings.TrimSpace(history.LastKnownGood) == "" {
		return packageHistory{}, errors.New("package history requires package, current, and last_known_good")
	}
	return history, nil
}

func loadPermissionPolicy(path string) (permissionPolicy, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return permissionPolicy{}, fmt.Errorf("read permission-drift policy: %w", err)
	}
	var policy permissionPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return permissionPolicy{}, fmt.Errorf("decode permission-drift policy: %w", err)
	}
	if normalizeLane(policy.Lane) != "permission-drift" {
		return permissionPolicy{}, fmt.Errorf("policy lane %q does not match permission-drift", policy.Lane)
	}
	targets, err := normalizePermissionTargets(policy)
	if err != nil {
		return permissionPolicy{}, err
	}
	policy.Targets = targets
	return policy, nil
}

func normalizePermissionTargets(policy permissionPolicy) ([]permissionTarget, error) {
	var targets []permissionTarget
	if len(policy.PermissionTargets) > 0 {
		for _, target := range policy.PermissionTargets {
			target.Path = strings.TrimSpace(target.Path)
			target.Kind = strings.TrimSpace(target.Kind)
			if target.Kind == "" {
				target.Kind = "dir"
			}
			target.Access = strings.TrimSpace(target.Access)
			target.ExpectedMode = firstNonEmpty(target.ExpectedMode, policy.ExpectedMode)
			target.ExpectedOwner = firstNonEmpty(target.ExpectedOwner, policy.ExpectedOwner)
			target.ExpectedGroup = firstNonEmpty(target.ExpectedGroup, policy.ExpectedGroup)
			if err := validatePermissionTarget(target); err != nil {
				return nil, err
			}
			targets = append(targets, target)
		}
		return targets, nil
	}

	if len(policy.AllowedPaths) == 0 {
		return nil, errors.New("permission-drift policy allowed_paths or permission_targets is required")
	}
	if strings.TrimSpace(policy.ExpectedMode) == "" {
		return nil, errors.New("permission-drift policy expected_mode is required")
	}
	for _, allowedPath := range policy.AllowedPaths {
		target := permissionTarget{
			Path:          strings.TrimSpace(allowedPath),
			Kind:          "dir",
			Access:        "write",
			ExpectedOwner: policy.ExpectedOwner,
			ExpectedGroup: policy.ExpectedGroup,
			ExpectedMode:  policy.ExpectedMode,
		}
		if err := validatePermissionTarget(target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func validatePermissionTarget(target permissionTarget) error {
	if target.Path == "" {
		return errors.New("permission-drift policy target path is required")
	}
	if target.Kind != "dir" && target.Kind != "file" {
		return fmt.Errorf("permission-drift policy target %q kind %q is unsupported", target.Path, target.Kind)
	}
	if target.Kind == "file" && target.Access != "read" && target.Access != "write" {
		return fmt.Errorf("permission-drift policy file target %q access %q is unsupported", target.Path, target.Access)
	}
	if target.Kind == "dir" && target.Access == "" {
		target.Access = "write"
	}
	if strings.TrimSpace(target.ExpectedMode) == "" {
		return fmt.Errorf("permission-drift policy target %q expected_mode is required", target.Path)
	}
	mode, err := parsePermissionMode(target.ExpectedMode)
	if err != nil {
		return err
	}
	if mode&0o002 != 0 {
		return fmt.Errorf("permission-drift policy target %q expected_mode %q is unsafe; world-writable modes such as 0777 are forbidden", target.Path, target.ExpectedMode)
	}
	return nil
}

func parsePermissionMode(value string) (os.FileMode, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := strconv.ParseUint(trimmed, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("parse expected_mode %q: %w", value, err)
	}
	if parsed > 0o777 {
		return 0, fmt.Errorf("permission-drift policy expected_mode %q exceeds file permission bits", value)
	}
	return os.FileMode(parsed), nil
}

func loadRestartPolicy(path string) (restartPolicy, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return restartPolicy{}, fmt.Errorf("read restart-reload policy: %w", err)
	}
	var policy restartPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return restartPolicy{}, fmt.Errorf("decode restart-reload policy: %w", err)
	}
	if normalizeLane(policy.Lane) != "restart-reload" {
		return restartPolicy{}, fmt.Errorf("policy lane %q does not match restart-reload", policy.Lane)
	}
	if len(policy.AllowedRestartTargets) == 0 {
		return restartPolicy{}, errors.New("restart-reload policy allowed_restart_targets is required")
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

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(target) {
			return true
		}
	}
	return false
}

func hasDockerTarget(input CandidateInput) bool {
	return strings.TrimSpace(input.ComposeFile) != "" &&
		strings.TrimSpace(input.ComposeProject) != "" &&
		strings.TrimSpace(input.DockerService) != ""
}

func repairFileParentSearchPermission(appDir string, target permissionTarget, policy permissionPolicy) error {
	parent, ok := fileParentSearchPath(target, policy)
	if !ok {
		return nil
	}
	parentPath, err := joinInsideApp(appDir, parent)
	if err != nil {
		return fmt.Errorf("resolve permission parent path: %w", err)
	}
	if err := os.Chmod(parentPath, 0o711); err != nil {
		return fmt.Errorf("chmod parent %s: %w", parent, err)
	}
	return nil
}

func fileParentSearchPath(target permissionTarget, policy permissionPolicy) (string, bool) {
	if target.Kind != "file" {
		return "", false
	}
	parent := filepath.Clean(filepath.Dir(filepath.Clean(target.Path)))
	if parent == "." || parent == string(filepath.Separator) {
		return "", false
	}
	for _, declared := range policy.Targets {
		if declared.Kind == "dir" && filepath.Clean(declared.Path) == parent {
			return "", false
		}
	}
	return parent, true
}

func dockerRepairPermissions(ctx context.Context, input CandidateInput, target permissionTarget, policy permissionPolicy) error {
	containerPath := "/app/" + filepath.ToSlash(filepath.Clean(target.Path))
	owner := firstNonEmpty(target.ExpectedOwner, policy.ExpectedOwner, "app")
	group := firstNonEmpty(target.ExpectedGroup, policy.ExpectedGroup, owner)
	mode := target.ExpectedMode
	var script string
	if target.Kind == "file" {
		if parent, ok := fileParentSearchPath(target, policy); ok {
			containerParent := "/app/" + filepath.ToSlash(parent)
			script = fmt.Sprintf("test -d %s && chmod 0711 %s && ", shellQuote(containerParent), shellQuote(containerParent))
		}
		if target.Access == "write" {
			script += fmt.Sprintf("if [ ! -e %s ]; then : > %s; fi && ", shellQuote(containerPath), shellQuote(containerPath))
		}
		script += fmt.Sprintf("test -f %s && chown %s:%s %s && chmod %s %s", shellQuote(containerPath), shellQuote(owner), shellQuote(group), shellQuote(containerPath), shellQuote(mode), shellQuote(containerPath))
	} else {
		script = fmt.Sprintf("mkdir -p %s && chown -R %s:%s %s && chmod %s %s", shellQuote(containerPath), shellQuote(owner), shellQuote(group), shellQuote(containerPath), shellQuote(mode), shellQuote(containerPath))
	}
	args := []string{"compose", "-f", input.ComposeFile, "-p", input.ComposeProject, "exec", "-T", "-u", "root", input.DockerService, "sh", "-lc", script}
	command := exec.CommandContext(ctx, "docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func dockerPackageVerifyCommand(input CandidateInput, packageFile string, verifyURL string, expectedStatus int, bodyContains string) string {
	compose := "docker compose -f " + shellQuote(input.ComposeFile) + " -p " + shellQuote(input.ComposeProject)
	target := input.DockerService + ":/app/package.json"
	steps := []string{
		compose + " cp " + shellQuote(packageFile) + " " + shellQuote(target),
		compose + " exec -T " + shellQuote(input.DockerService) + " npm install --omit=dev",
		compose + " restart " + shellQuote(input.DockerService),
		pythonHTTPVerifyCommand(verifyURL, expectedStatus, bodyContains),
	}
	return strings.Join(steps, " && ")
}

func pythonHTTPVerifyCommand(verifyURL string, expectedStatus int, bodyContains string) string {
	code := strings.Join([]string{
		"import sys,time,urllib.request,urllib.error",
		"url=" + strconv.Quote(verifyURL),
		"expected=" + strconv.Itoa(expectedStatus),
		"needle=" + strconv.Quote(bodyContains),
		"deadline=time.time()+20",
		"last=''",
		"status=0",
		"body=''",
		"while time.time()<deadline:",
		"    try:",
		"        r=urllib.request.urlopen(url, timeout=3); status=r.status; body=r.read().decode('utf-8','replace')",
		"    except urllib.error.HTTPError as e:",
		"        status=e.code; body=e.read().decode('utf-8','replace')",
		"    except Exception as e:",
		"        last=repr(e); time.sleep(1); continue",
		"    if status==expected and (not needle or needle in body): sys.exit(0)",
		"    last=f'status={status} body={body[:200]}'; time.sleep(1)",
		"print(last, file=sys.stderr)",
		"sys.exit(1)",
	}, "\n")
	return "python3 -c " + shellQuote(code)
}

func verifyLiveProbe(ctx context.Context, input CandidateInput, verification policyVerification) error {
	verifyURL := firstNonEmpty(input.LiveProbeURL, verification.URL)
	if verifyURL == "" {
		return errors.New("live probe URL is required for verification")
	}
	expectedStatus := firstNonZero(input.ExpectedFixedStatus, verification.ExpectedStatus, http.StatusOK)
	bodyContains := firstNonEmpty(input.FixedBodyContains, verification.BodyContains)
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for {
		status, body, err := getHTTP(ctx, verifyURL)
		if err == nil && status == expectedStatus && (bodyContains == "" || strings.Contains(body, bodyContains)) {
			return nil
		}
		if err != nil {
			last = err.Error()
		} else {
			last = fmt.Sprintf("status=%d body=%s", status, safeBody(body))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("verify live probe %s failed: %s", verifyURL, last)
		}
		time.Sleep(time.Second)
	}
}

func getHTTP(ctx context.Context, verifyURL string) (int, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, verifyURL, nil)
	if err != nil {
		return 0, "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return response.StatusCode, "", err
	}
	return response.StatusCode, string(raw), nil
}

func safeBody(body string) string {
	body = strings.TrimSpace(body)
	if len(body) > 200 {
		return body[:200]
	}
	return body
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
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
