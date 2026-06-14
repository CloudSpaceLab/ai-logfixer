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
	"syscall"
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
	SchemaVersion     string                          `json:"schema_version"`
	ScenarioID        string                          `json:"scenario_id,omitempty"`
	OperationalLane   string                          `json:"operational_lane,omitempty"`
	Supported         bool                            `json:"supported"`
	Status            Status                          `json:"status"`
	Message           string                          `json:"message"`
	RemediationPlan   *contractsv1.RemediationPlan    `json:"remediation_plan,omitempty"`
	Attempt           *contractsv1.RemediationAttempt `json:"attempt,omitempty"`
	Receipt           *contractsv1.Receipt            `json:"receipt,omitempty"`
	BackupPath        string                          `json:"backup_path,omitempty"`
	RollbackPath      string                          `json:"rollback_path,omitempty"`
	PermissionChanges []PermissionChange              `json:"permission_changes,omitempty"`
}

type PermissionChange struct {
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Access        string `json:"access,omitempty"`
	Action        string `json:"action"`
	ExpectedOwner string `json:"expected_owner,omitempty"`
	ExpectedGroup string `json:"expected_group,omitempty"`
	ExpectedMode  string `json:"expected_mode"`
	BeforeExists  bool   `json:"before_exists"`
	BeforeMode    string `json:"before_mode,omitempty"`
	BeforeOwner   string `json:"before_owner,omitempty"`
	BeforeGroup   string `json:"before_group,omitempty"`
	AfterExists   bool   `json:"after_exists"`
	AfterMode     string `json:"after_mode,omitempty"`
	AfterOwner    string `json:"after_owner,omitempty"`
	AfterGroup    string `json:"after_group,omitempty"`
}

type permissionState struct {
	Exists bool
	IsDir  bool
	Mode   string
	Owner  string
	Group  string
}

type permissionRollbackManifest struct {
	SchemaVersion string                            `json:"schema_version"`
	ScenarioID    string                            `json:"scenario_id"`
	CreatedAt     time.Time                         `json:"created_at"`
	Changes       []permissionRollbackManifestEntry `json:"changes"`
}

type permissionRollbackManifestEntry struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	BeforeExists bool   `json:"before_exists"`
	BeforeMode   string `json:"before_mode,omitempty"`
	BeforeOwner  string `json:"before_owner,omitempty"`
	BeforeGroup  string `json:"before_group,omitempty"`
	AfterExists  bool   `json:"after_exists"`
	AfterMode    string `json:"after_mode,omitempty"`
	AfterOwner   string `json:"after_owner,omitempty"`
	AfterGroup   string `json:"after_group,omitempty"`
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
	var changes []PermissionChange
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
			targetChanges, err := dockerRepairPermissions(ctx, input, target, policy)
			if err != nil {
				return Response{}, err
			}
			changes = append(changes, targetChanges...)
			continue
		}
		targetChanges, err := repairLocalPermissions(input.AppDir, target, policy, mode)
		if err != nil {
			return Response{}, err
		}
		changes = append(changes, targetChanges...)
	}
	rollbackPath, err := writePermissionRollbackManifest(input, changes)
	if err != nil {
		return Response{}, err
	}
	if err := verifyLiveProbe(ctx, input, policy.Verification); err != nil {
		return Response{}, err
	}
	response := baseResponse(input, StatusResolved, true, "permission-drift resolver completed")
	response.PermissionChanges = changes
	response.RollbackPath = rollbackPath
	response.RemediationPlan, response.Attempt, response.Receipt = buildPermissionResolutionContracts(input, changes, rollbackPath)
	return response, nil
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

func repairLocalPermissions(appDir string, target permissionTarget, policy permissionPolicy, mode os.FileMode) ([]PermissionChange, error) {
	path, err := joinInsideApp(appDir, target.Path)
	if err != nil {
		return nil, err
	}
	var changes []PermissionChange
	switch target.Kind {
	case "dir":
		before, err := localPermissionState(appDir, target.Path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(path, mode); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", target.Path, err)
		}
		if err := os.Chmod(path, mode); err != nil {
			return nil, fmt.Errorf("chmod %s: %w", target.Path, err)
		}
		after, err := localPermissionState(appDir, target.Path)
		if err != nil {
			return nil, err
		}
		changes = append(changes, newPermissionChange(target, "repair_dir_permissions", before, after))
	case "file":
		parentChange, err := repairFileParentSearchPermission(appDir, target, policy)
		if err != nil {
			return nil, err
		}
		if parentChange != nil {
			changes = append(changes, *parentChange)
		}
		before, err := localPermissionState(appDir, target.Path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		created := false
		if os.IsNotExist(err) && target.Access == "write" {
			file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if createErr != nil {
				return nil, fmt.Errorf("create writable file target %s: %w", target.Path, createErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				return nil, fmt.Errorf("close writable file target %s: %w", target.Path, closeErr)
			}
			created = true
			info, err = os.Stat(path)
		}
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", target.Path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("permission target %s is a directory, expected file", target.Path)
		}
		if err := os.Chmod(path, mode); err != nil {
			return nil, fmt.Errorf("chmod %s: %w", target.Path, err)
		}
		after, err := localPermissionState(appDir, target.Path)
		if err != nil {
			return nil, err
		}
		action := "repair_file_permissions"
		if created {
			action = "create_writable_file"
		}
		changes = append(changes, newPermissionChange(target, action, before, after))
	default:
		return nil, fmt.Errorf("permission target %s has unsupported kind %q", target.Path, target.Kind)
	}
	return changes, nil
}

func repairFileParentSearchPermission(appDir string, target permissionTarget, policy permissionPolicy) (*PermissionChange, error) {
	parent, ok := fileParentSearchPath(target, policy)
	if !ok {
		return nil, nil
	}
	parentPath, err := joinInsideApp(appDir, parent)
	if err != nil {
		return nil, fmt.Errorf("resolve permission parent path: %w", err)
	}
	before, err := localPermissionState(appDir, parent)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(parentPath, 0o711); err != nil {
		return nil, fmt.Errorf("chmod parent %s: %w", parent, err)
	}
	after, err := localPermissionState(appDir, parent)
	if err != nil {
		return nil, err
	}
	parentTarget := permissionTarget{
		Path:          parent,
		Kind:          "dir",
		Access:        "search",
		ExpectedOwner: target.ExpectedOwner,
		ExpectedGroup: target.ExpectedGroup,
		ExpectedMode:  "0711",
	}
	change := newPermissionChange(parentTarget, "repair_parent_search_permission", before, after)
	return &change, nil
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

func localPermissionState(appDir string, relativePath string) (permissionState, error) {
	path, err := joinInsideApp(appDir, relativePath)
	if err != nil {
		return permissionState{}, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return permissionState{Exists: false}, nil
	}
	if err != nil {
		return permissionState{}, fmt.Errorf("stat %s: %w", relativePath, err)
	}
	state := permissionState{
		Exists: true,
		IsDir:  info.IsDir(),
		Mode:   formatPermissionMode(info.Mode().Perm()),
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		state.Owner = strconv.FormatUint(uint64(stat.Uid), 10)
		state.Group = strconv.FormatUint(uint64(stat.Gid), 10)
	}
	return state, nil
}

func dockerPermissionState(ctx context.Context, input CandidateInput, relativePath string) (permissionState, error) {
	containerPath := "/app/" + filepath.ToSlash(filepath.Clean(relativePath))
	script := fmt.Sprintf("if [ -e %s ]; then stat -c 'exists\t%%F\t%%a\t%%u\t%%g' %s; else echo missing; fi", shellQuote(containerPath), shellQuote(containerPath))
	args := []string{"compose", "-f", input.ComposeFile, "-p", input.ComposeProject, "exec", "-T", "-u", "root", input.DockerService, "sh", "-lc", script}
	command := exec.CommandContext(ctx, "docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return permissionState{}, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "missing" {
		return permissionState{Exists: false}, nil
	}
	fields := strings.Split(trimmed, "\t")
	if len(fields) != 5 || fields[0] != "exists" {
		return permissionState{}, fmt.Errorf("parse docker permission state for %s: %q", relativePath, trimmed)
	}
	return permissionState{
		Exists: true,
		IsDir:  strings.Contains(strings.ToLower(fields[1]), "directory"),
		Mode:   normalizeModeString(fields[2]),
		Owner:  fields[3],
		Group:  fields[4],
	}, nil
}

func newPermissionChange(target permissionTarget, action string, before permissionState, after permissionState) PermissionChange {
	return PermissionChange{
		Path:          filepath.ToSlash(filepath.Clean(target.Path)),
		Kind:          target.Kind,
		Access:        target.Access,
		Action:        action,
		ExpectedOwner: target.ExpectedOwner,
		ExpectedGroup: target.ExpectedGroup,
		ExpectedMode:  normalizeModeString(target.ExpectedMode),
		BeforeExists:  before.Exists,
		BeforeMode:    before.Mode,
		BeforeOwner:   before.Owner,
		BeforeGroup:   before.Group,
		AfterExists:   after.Exists,
		AfterMode:     after.Mode,
		AfterOwner:    after.Owner,
		AfterGroup:    after.Group,
	}
}

func writePermissionRollbackManifest(input CandidateInput, changes []PermissionChange) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}
	root := permissionRollbackRoot(input)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create permission rollback directory: %w", err)
	}
	manifest := permissionRollbackManifest{
		SchemaVersion: "ai-logfixer-permission-rollback/v1",
		ScenarioID:    input.ScenarioID,
		CreatedAt:     time.Now().UTC(),
		Changes:       make([]permissionRollbackManifestEntry, 0, len(changes)),
	}
	for _, change := range changes {
		manifest.Changes = append(manifest.Changes, permissionRollbackManifestEntry{
			Path:         change.Path,
			Kind:         change.Kind,
			BeforeExists: change.BeforeExists,
			BeforeMode:   change.BeforeMode,
			BeforeOwner:  change.BeforeOwner,
			BeforeGroup:  change.BeforeGroup,
			AfterExists:  change.AfterExists,
			AfterMode:    change.AfterMode,
			AfterOwner:   change.AfterOwner,
			AfterGroup:   change.AfterGroup,
		})
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode permission rollback manifest: %w", err)
	}
	path := filepath.Join(root, sanitizeFilename(firstNonEmpty(input.ScenarioID, "permission-drift"))+"-rollback.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("write permission rollback manifest: %w", err)
	}
	return path, nil
}

func permissionRollbackRoot(input CandidateInput) string {
	if strings.TrimSpace(input.InventoryDir) != "" {
		return filepath.Join(filepath.Dir(filepath.Clean(input.InventoryDir)), "permission-rollbacks")
	}
	return filepath.Join(input.AppDir, ".ai-logfixer", "permission-rollbacks")
}

func buildPermissionResolutionContracts(input CandidateInput, changes []PermissionChange, rollbackPath string) (*contractsv1.RemediationPlan, *contractsv1.RemediationAttempt, *contractsv1.Receipt) {
	now := time.Now().UTC()
	finished := now.Add(time.Second)
	idBase := sanitizeFilename(firstNonEmpty(input.ScenarioID, input.ServiceName, "permission-drift"))
	planID := "rem-plan-permission-drift-" + idBase
	attemptID := "rem-attempt-permission-drift-" + idBase
	receiptID := "receipt-permission-drift-" + idBase
	rollbackRefs := []string{}
	if rollbackPath != "" {
		rollbackRefs = append(rollbackRefs, rollbackPath)
	}
	before := permissionChangesBeforeSummary(changes)
	after := permissionChangesAfterSummary(changes)
	plan := &contractsv1.RemediationPlan{
		ID:                planID,
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: "diag-permission-drift-" + idBase,
		Summary:           "Apply bounded permission-drift repairs to allowlisted targets.",
		FixPreview:        contractsv1.DiffPreview{Before: before, After: permissionChangesActionSummary(changes)},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:                   "rollback-permission-drift-" + idBase,
			RollbackType:         contractsv1.RollbackReversePatch,
			SnapshotRefs:         rollbackRefs,
			RestoreSteps:         []string{"Restore recorded owner/group/mode state from the permission rollback manifest.", "Re-run permission probes.", "Verify service health."},
			Limitations:          []string{"Rollback manifest records exact path state; automated rollback execution is handled outside this readiness resolver."},
			RiskLevel:            contractsv1.SafetyLowRisk,
			RequiresManualReview: false,
		},
		RiskLevel:        contractsv1.SafetyLowRisk,
		ApprovalRequired: false,
		Status:           contractsv1.RemediationStatusSucceeded,
		DisplayStatus:    "Permission repair verified",
		UserMessage:      "AI LogFixer repaired allowlisted permission drift and recorded rollback evidence.",
		NextActions:      []contractsv1.NextAction{},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: "tl-permission-plan-" + idBase, Type: "remediation.plan_created", Message: "Permission remediation plan recorded.", Severity: "info", Timestamp: now}},
		ExternalRefs:     []contractsv1.ExternalRef{},
		KnowledgeRefs:    []contractsv1.KnowledgeRef{},
		CreatedAt:        now,
	}
	attempt := &contractsv1.RemediationAttempt{
		ID:                  attemptID,
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "auto-approved-readiness-permission-drift",
		Status:              contractsv1.RemediationStatusSucceeded,
		ExecutionStartedAt:  &now,
		ExecutionFinishedAt: &finished,
		MonitorSummary: contractsv1.MonitorSummary{
			Status:   "healthy",
			Message:  "Permission drift repair verified by live probe.",
			Signals:  []string{"permission_changes=" + strconv.Itoa(len(changes)), "service=" + input.ServiceName},
			Duration: "1s",
		},
		DisplayStatus:  "Permission repair verified",
		UserMessage:    "AI LogFixer verified the app after permission repair.",
		TimelineEvents: []contractsv1.TimelineEvent{{ID: "tl-permission-attempt-" + idBase, Type: "remediation.succeeded", Message: "Permission remediation attempt succeeded.", Severity: "info", Timestamp: finished}},
		ExternalRefs:   []contractsv1.ExternalRef{},
	}
	receipt := &contractsv1.Receipt{
		ID:                   receiptID,
		DiagnosisID:          "diag-permission-drift-" + idBase,
		RemediationPlanID:    planID,
		RemediationAttemptID: attemptID,
		ActionTaken:          permissionChangesActionSummary(changes),
		Actor:                "ai-logfixer-readiness-resolve",
		Approver:             "auto-approved-readiness-permission-drift",
		Timestamp:            finished,
		BeforeState:          before,
		AfterState:           after,
		Outcome:              "succeeded",
		Summary:              "AI LogFixer repaired permission drift, verified recovery, and wrote rollback evidence.",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: "tl-permission-receipt-" + idBase, Type: "receipt.created", Message: "Permission remediation receipt recorded.", Severity: "info", Timestamp: finished}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
	return plan, attempt, receipt
}

func permissionChangesBeforeSummary(changes []PermissionChange) string {
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		parts = append(parts, fmt.Sprintf("%s exists=%t mode=%s owner=%s group=%s", change.Path, change.BeforeExists, change.BeforeMode, change.BeforeOwner, change.BeforeGroup))
	}
	return strings.Join(parts, "; ")
}

func permissionChangesAfterSummary(changes []PermissionChange) string {
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		parts = append(parts, fmt.Sprintf("%s exists=%t mode=%s owner=%s group=%s", change.Path, change.AfterExists, change.AfterMode, change.AfterOwner, change.AfterGroup))
	}
	return strings.Join(parts, "; ")
}

func permissionChangesActionSummary(changes []PermissionChange) string {
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		parts = append(parts, fmt.Sprintf("%s %s %s->%s", change.Action, change.Path, defaultString(change.BeforeMode, "missing"), defaultString(change.AfterMode, "missing")))
	}
	return strings.Join(parts, "; ")
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func formatPermissionMode(mode os.FileMode) string {
	return fmt.Sprintf("%04o", mode.Perm())
}

func normalizeModeString(mode string) string {
	trimmed := strings.TrimSpace(mode)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) >= 4 {
		return trimmed
	}
	return strings.Repeat("0", 4-len(trimmed)) + trimmed
}

func sanitizeFilename(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			continue
		}
		if r == '-' || r == '_' || r == '.' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('-')
	}
	out := strings.Trim(builder.String(), "-.")
	if out == "" {
		return "permission-drift"
	}
	return out
}

func dockerRepairPermissions(ctx context.Context, input CandidateInput, target permissionTarget, policy permissionPolicy) ([]PermissionChange, error) {
	containerPath := "/app/" + filepath.ToSlash(filepath.Clean(target.Path))
	owner := firstNonEmpty(target.ExpectedOwner, policy.ExpectedOwner, "app")
	group := firstNonEmpty(target.ExpectedGroup, policy.ExpectedGroup, owner)
	mode := target.ExpectedMode
	var changes []PermissionChange
	var parentTarget permissionTarget
	var parentBefore permissionState
	hasParentChange := false
	var script string
	if target.Kind == "file" {
		if parent, ok := fileParentSearchPath(target, policy); ok {
			containerParent := "/app/" + filepath.ToSlash(parent)
			before, err := dockerPermissionState(ctx, input, parent)
			if err != nil {
				return nil, err
			}
			parentBefore = before
			script = fmt.Sprintf("test -d %s && chmod 0711 %s && ", shellQuote(containerParent), shellQuote(containerParent))
			parentTarget = permissionTarget{
				Path:          parent,
				Kind:          "dir",
				Access:        "search",
				ExpectedOwner: target.ExpectedOwner,
				ExpectedGroup: target.ExpectedGroup,
				ExpectedMode:  "0711",
			}
			hasParentChange = true
		}
		if target.Access == "write" {
			script += fmt.Sprintf("if [ ! -e %s ]; then : > %s; fi && ", shellQuote(containerPath), shellQuote(containerPath))
		}
		script += fmt.Sprintf("test -f %s && chown %s:%s %s && chmod %s %s", shellQuote(containerPath), shellQuote(owner), shellQuote(group), shellQuote(containerPath), shellQuote(mode), shellQuote(containerPath))
	} else {
		script = fmt.Sprintf("mkdir -p %s && chown %s:%s %s && chmod %s %s", shellQuote(containerPath), shellQuote(owner), shellQuote(group), shellQuote(containerPath), shellQuote(mode), shellQuote(containerPath))
	}
	before, err := dockerPermissionState(ctx, input, target.Path)
	if err != nil {
		return nil, err
	}
	args := []string{"compose", "-f", input.ComposeFile, "-p", input.ComposeProject, "exec", "-T", "-u", "root", input.DockerService, "sh", "-lc", script}
	command := exec.CommandContext(ctx, "docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	after, err := dockerPermissionState(ctx, input, target.Path)
	if err != nil {
		return nil, err
	}
	if hasParentChange {
		parentAfter, err := dockerPermissionState(ctx, input, parentTarget.Path)
		if err != nil {
			return nil, err
		}
		changes = append(changes, newPermissionChange(parentTarget, "repair_parent_search_permission", parentBefore, parentAfter))
	}
	action := "repair_dir_permissions"
	if target.Kind == "file" {
		action = "repair_file_permissions"
		if !before.Exists && target.Access == "write" {
			action = "create_writable_file"
		}
	}
	changes = append(changes, newPermissionChange(target, action, before, after))
	return changes, nil
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
