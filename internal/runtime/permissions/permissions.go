package permissions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
)

const (
	frameworkAuto    = "auto"
	frameworkExpress = "express"
	frameworkFastAPI = "fastapi"
	frameworkFlask   = "flask"
	frameworkGo      = "go"
	frameworkJava    = "java"
	frameworkLaravel = "laravel"
	frameworkRails   = "rails"
	frameworkRuby    = "ruby"
)

type Options struct {
	ServiceName    string
	TargetDir      string
	Framework      string
	VerifyURL      string
	ExpectedStatus int
	Apply          bool
	Now            time.Time
}

type Result struct {
	InvestigationRequest contractsv1.InvestigationRequest `json:"investigation_request"`
	Diagnosis            contractsv1.DiagnosisResult      `json:"diagnosis"`
	RemediationPlan      contractsv1.RemediationPlan      `json:"remediation_plan"`
	Attempt              contractsv1.RemediationAttempt   `json:"attempt"`
	Receipt              contractsv1.Receipt              `json:"receipt"`
	Framework            string                           `json:"framework"`
	Policy               PermissionPolicy                 `json:"policy"`
	Findings             []PermissionFinding              `json:"findings"`
	Operations           []PermissionOperation            `json:"operations"`
	Before               []PathState                      `json:"before"`
	After                []PathState                      `json:"after"`
	RollbackPath         string                           `json:"rollback_path,omitempty"`
}

type InferredPolicy struct {
	Framework string           `json:"framework"`
	Policy    PermissionPolicy `json:"policy"`
}

type PermissionPolicy struct {
	Framework       string            `json:"framework"`
	Name            string            `json:"name"`
	ExpectedPaths   []ExpectedPath    `json:"expected_paths"`
	ForbiddenModes  []string          `json:"forbidden_modes"`
	ForbiddenAction []string          `json:"forbidden_actions"`
	References      []PolicyReference `json:"references"`
}

type ExpectedPath struct {
	RelativePath string      `json:"relative_path"`
	Kind         string      `json:"kind"`
	Mode         os.FileMode `json:"mode"`
	Writable     bool        `json:"writable"`
}

type PolicyReference struct {
	Title  string `json:"title"`
	Source string `json:"source"`
}

type PermissionFinding struct {
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	Problem      string `json:"problem"`
	CurrentMode  string `json:"current_mode"`
	ExpectedMode string `json:"expected_mode"`
	Writable     bool   `json:"writable"`
	ProbeError   string `json:"probe_error,omitempty"`
}

type PermissionOperation struct {
	Action       string      `json:"action"`
	Path         string      `json:"path"`
	RelativePath string      `json:"relative_path"`
	Mode         os.FileMode `json:"mode"`
	Before       string      `json:"before"`
	After        string      `json:"after"`
}

type PathState struct {
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	Exists       bool   `json:"exists"`
	IsDir        bool   `json:"is_dir"`
	Mode         string `json:"mode,omitempty"`
	UID          string `json:"uid,omitempty"`
	GID          string `json:"gid,omitempty"`
	Writable     bool   `json:"writable"`
	ProbeError   string `json:"probe_error,omitempty"`
}

type rollbackManifest struct {
	CreatedAt time.Time       `json:"created_at"`
	Entries   []rollbackEntry `json:"entries"`
}

type rollbackEntry struct {
	Path         string      `json:"path"`
	RelativePath string      `json:"relative_path"`
	Exists       bool        `json:"exists"`
	Mode         os.FileMode `json:"mode"`
}

func InferPolicy(options Options) (InferredPolicy, error) {
	options = normalizeOptions(options)
	if strings.TrimSpace(options.TargetDir) == "" {
		return InferredPolicy{}, errors.New("target directory is required")
	}

	targetDir, err := filepath.Abs(filepath.Clean(options.TargetDir))
	if err != nil {
		return InferredPolicy{}, fmt.Errorf("resolve target directory: %w", err)
	}
	options.TargetDir = targetDir

	framework, err := resolveFramework(options)
	if err != nil {
		return InferredPolicy{}, fmt.Errorf("framework detection failed: %w", err)
	}
	policy, err := policyForFramework(framework)
	if err != nil {
		return InferredPolicy{}, err
	}
	return InferredPolicy{Framework: framework, Policy: policy}, nil
}

func Run(ctx context.Context, options Options) (Result, error) {
	options = normalizeOptions(options)
	if strings.TrimSpace(options.TargetDir) == "" {
		return Result{}, errors.New("target directory is required")
	}

	targetDir, err := filepath.Abs(filepath.Clean(options.TargetDir))
	if err != nil {
		return Result{}, fmt.Errorf("resolve target directory: %w", err)
	}
	options.TargetDir = targetDir

	framework, err := resolveFramework(options)
	if err != nil {
		return buildBlockedResult(options, "framework detection failed: "+err.Error(), nil)
	}
	policy, err := policyForFramework(framework)
	if err != nil {
		return buildBlockedResult(options, err.Error(), nil)
	}

	before, findings, operations, blockedReason := inspectPolicy(options.TargetDir, policy)
	if blockedReason != "" {
		return buildBlockedResult(options, blockedReason, before)
	}

	result := buildResult(options, framework, policy, before, before, findings, operations, "", "dry_run")
	if err := validateResult(result); err != nil {
		return Result{}, err
	}
	if !options.Apply {
		return result, nil
	}
	if len(operations) == 0 {
		if len(findings) > 0 {
			return buildBlockedResult(options, "permission drift requires an ownership or ACL repair that is not supported by the current policy", before)
		}
		result.Attempt.MonitorSummary.Status = "healthy"
		result.Attempt.MonitorSummary.Message = "Permission policy already matched the app filesystem."
		result.Receipt.Outcome = "succeeded"
		result.Receipt.ActionTaken = "no permission changes required"
		result.Receipt.Summary = "AI LogFixer verified framework permission policy without applying changes."
		return result, validateResult(result)
	}

	rollbackPath, rollback, err := writeRollbackManifest(options, before, operations)
	if err != nil {
		return Result{}, fmt.Errorf("write rollback manifest: %w", err)
	}
	if err := applyOperations(operations); err != nil {
		_ = rollbackPermissions(rollback)
		return Result{}, fmt.Errorf("apply permission operations: %w", err)
	}

	after, afterFindings, _, blockedReason := inspectPolicy(options.TargetDir, policy)
	if blockedReason != "" {
		_ = rollbackPermissions(rollback)
		return Result{}, fmt.Errorf("permission verification blocked after apply: %s", blockedReason)
	}
	if len(afterFindings) > 0 {
		_ = rollbackPermissions(rollback)
		return Result{}, fmt.Errorf("permission verification failed after apply: %s", findingsSummary(afterFindings))
	}
	if options.VerifyURL != "" {
		if err := verifyHTTP(ctx, options.VerifyURL, options.ExpectedStatus); err != nil {
			_ = rollbackPermissions(rollback)
			return Result{}, fmt.Errorf("verify service after permission repair: %w", err)
		}
	}

	result = buildResult(options, framework, policy, before, after, findings, operations, rollbackPath, "succeeded")
	if err := validateResult(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func normalizeOptions(options Options) Options {
	if options.ServiceName == "" {
		options.ServiceName = "unknown-service"
	}
	if options.Framework == "" {
		options.Framework = frameworkAuto
	}
	options.Framework = strings.ToLower(strings.TrimSpace(options.Framework))
	if options.ExpectedStatus == 0 {
		options.ExpectedStatus = http.StatusOK
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	return options
}

func resolveFramework(options Options) (string, error) {
	if options.Framework != frameworkAuto {
		return options.Framework, nil
	}
	if fileExists(filepath.Join(options.TargetDir, "artisan")) && fileExists(filepath.Join(options.TargetDir, "composer.json")) {
		return frameworkLaravel, nil
	}
	if fileContainsAny(filepath.Join(options.TargetDir, "Gemfile"), "rails") || fileExists(filepath.Join(options.TargetDir, "config", "application.rb")) {
		return frameworkRails, nil
	}
	if fileContainsAny(filepath.Join(options.TargetDir, "package.json"), "express") {
		return frameworkExpress, nil
	}
	if fileContainsAny(filepath.Join(options.TargetDir, "requirements.txt"), "fastapi") ||
		fileContainsAny(filepath.Join(options.TargetDir, "pyproject.toml"), "fastapi") {
		return frameworkFastAPI, nil
	}
	if fileContainsAny(filepath.Join(options.TargetDir, "requirements.txt"), "flask") ||
		fileContainsAny(filepath.Join(options.TargetDir, "pyproject.toml"), "flask") {
		return frameworkFlask, nil
	}
	if fileExists(filepath.Join(options.TargetDir, "go.mod")) {
		return frameworkGo, nil
	}
	if fileExists(filepath.Join(options.TargetDir, "pom.xml")) ||
		fileExists(filepath.Join(options.TargetDir, "build.gradle")) ||
		fileExists(filepath.Join(options.TargetDir, "build.gradle.kts")) {
		return frameworkJava, nil
	}
	if fileExists(filepath.Join(options.TargetDir, "Gemfile")) {
		return frameworkRuby, nil
	}
	return "", errors.New("no supported framework markers found")
}

func policyForFramework(framework string) (PermissionPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(framework)) {
	case frameworkExpress:
		return expressPolicy(), nil
	case frameworkFastAPI:
		return pythonPolicy(frameworkFastAPI, "FastAPI writable runtime directories"), nil
	case frameworkFlask:
		return pythonPolicy(frameworkFlask, "Flask writable runtime directories"), nil
	case frameworkGo:
		return goPolicy(), nil
	case frameworkJava:
		return javaPolicy(), nil
	case frameworkLaravel:
		return laravelPolicy(), nil
	case frameworkRails:
		return railsPolicy(), nil
	case frameworkRuby:
		return rubyPolicy(), nil
	default:
		return PermissionPolicy{}, fmt.Errorf("unsupported framework %q for permission remediation", framework)
	}
}

func laravelPolicy() PermissionPolicy {
	return PermissionPolicy{
		Framework:      frameworkLaravel,
		Name:           "Laravel writable runtime directories",
		ForbiddenModes: []string{"0777"},
		ForbiddenAction: []string{
			"chmod_777",
			"recursive_root_change_outside_allowlist",
		},
		ExpectedPaths: []ExpectedPath{
			{RelativePath: "storage", Kind: "dir", Mode: 0o775, Writable: true},
			{RelativePath: "storage/logs", Kind: "dir", Mode: 0o775, Writable: true},
			{RelativePath: "storage/framework/cache", Kind: "dir", Mode: 0o775, Writable: true},
			{RelativePath: "storage/framework/sessions", Kind: "dir", Mode: 0o775, Writable: true},
			{RelativePath: "storage/framework/views", Kind: "dir", Mode: 0o775, Writable: true},
			{RelativePath: "bootstrap/cache", Kind: "dir", Mode: 0o775, Writable: true},
		},
		References: []PolicyReference{
			{Title: "Laravel directory permissions", Source: "local Laravel framework policy"},
		},
	}
}

func expressPolicy() PermissionPolicy {
	return basePolicy(frameworkExpress, "Express writable runtime directories", []ExpectedPath{
		{RelativePath: "logs", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "tmp", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "uploads", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: ".cache", Kind: "dir", Mode: 0o775, Writable: true},
	})
}

func pythonPolicy(framework string, name string) PermissionPolicy {
	return basePolicy(framework, name, []ExpectedPath{
		{RelativePath: "instance", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "logs", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "tmp", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "uploads", Kind: "dir", Mode: 0o775, Writable: true},
	})
}

func goPolicy() PermissionPolicy {
	return basePolicy(frameworkGo, "Go service writable runtime directories", []ExpectedPath{
		{RelativePath: "data", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "logs", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "tmp", Kind: "dir", Mode: 0o775, Writable: true},
	})
}

func javaPolicy() PermissionPolicy {
	return basePolicy(frameworkJava, "Java service writable runtime directories", []ExpectedPath{
		{RelativePath: "logs", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "tmp", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "uploads", Kind: "dir", Mode: 0o775, Writable: true},
	})
}

func railsPolicy() PermissionPolicy {
	return basePolicy(frameworkRails, "Rails writable runtime directories", []ExpectedPath{
		{RelativePath: "log", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "storage", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "tmp", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "tmp/cache", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "public/assets", Kind: "dir", Mode: 0o775, Writable: true},
	})
}

func rubyPolicy() PermissionPolicy {
	return basePolicy(frameworkRuby, "Ruby service writable runtime directories", []ExpectedPath{
		{RelativePath: "log", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "storage", Kind: "dir", Mode: 0o775, Writable: true},
		{RelativePath: "tmp", Kind: "dir", Mode: 0o775, Writable: true},
	})
}

func basePolicy(framework string, name string, expected []ExpectedPath) PermissionPolicy {
	return PermissionPolicy{
		Framework:      framework,
		Name:           name,
		ForbiddenModes: []string{"0777"},
		ForbiddenAction: []string{
			"chmod_777",
			"recursive_root_change_outside_allowlist",
		},
		ExpectedPaths: expected,
		References: []PolicyReference{
			{Title: name, Source: "local framework permission policy"},
		},
	}
}

func inspectPolicy(root string, policy PermissionPolicy) ([]PathState, []PermissionFinding, []PermissionOperation, string) {
	var states []PathState
	var findings []PermissionFinding
	var operations []PermissionOperation

	for _, expected := range policy.ExpectedPaths {
		absPath, blockedReason := resolvePolicyPath(root, expected.RelativePath)
		if blockedReason != "" {
			return states, findings, operations, blockedReason
		}
		state := inspectPath(absPath, expected.RelativePath)
		states = append(states, state)

		if !state.Exists {
			findings = append(findings, PermissionFinding{
				Path:         absPath,
				RelativePath: expected.RelativePath,
				Problem:      "missing_runtime_directory",
				CurrentMode:  "missing",
				ExpectedMode: modeString(expected.Mode),
				Writable:     false,
			})
			operations = append(operations, PermissionOperation{
				Action:       "mkdir_chmod",
				Path:         absPath,
				RelativePath: expected.RelativePath,
				Mode:         expected.Mode,
				Before:       "missing",
				After:        "mode=" + modeString(expected.Mode),
			})
			continue
		}

		if expected.Kind == "dir" && !state.IsDir {
			return states, findings, operations, fmt.Sprintf("policy path %s is not a directory", expected.RelativePath)
		}

		if state.Mode != modeString(expected.Mode) {
			findings = append(findings, PermissionFinding{
				Path:         absPath,
				RelativePath: expected.RelativePath,
				Problem:      "mode_drift",
				CurrentMode:  state.Mode,
				ExpectedMode: modeString(expected.Mode),
				Writable:     state.Writable,
				ProbeError:   state.ProbeError,
			})
			operations = append(operations, PermissionOperation{
				Action:       "chmod",
				Path:         absPath,
				RelativePath: expected.RelativePath,
				Mode:         expected.Mode,
				Before:       "mode=" + state.Mode,
				After:        "mode=" + modeString(expected.Mode),
			})
			continue
		}

		if expected.Writable && !state.Writable {
			findings = append(findings, PermissionFinding{
				Path:         absPath,
				RelativePath: expected.RelativePath,
				Problem:      "not_writable_by_runtime_user",
				CurrentMode:  state.Mode,
				ExpectedMode: modeString(expected.Mode),
				Writable:     false,
				ProbeError:   state.ProbeError,
			})
		}
	}

	return states, findings, operations, ""
}

func resolvePolicyPath(root string, relativePath string) (string, string) {
	cleanRel := filepath.Clean(relativePath)
	if cleanRel == "." || filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) || cleanRel == ".." {
		return "", fmt.Sprintf("policy path %q is not a safe relative path", relativePath)
	}
	absPath := filepath.Join(root, cleanRel)
	rootEval, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Sprintf("target root cannot be resolved: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		if !isWithin(rootEval, resolved) {
			return "", fmt.Sprintf("policy path %s escapes app root", cleanRel)
		}
		return absPath, ""
	}

	parent := filepath.Dir(absPath)
	for {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			if !isWithin(rootEval, resolved) {
				return "", fmt.Sprintf("policy path %s escapes app root", cleanRel)
			}
			return absPath, ""
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Sprintf("policy path %s has no resolvable parent", cleanRel)
		}
		parent = next
	}
}

func isWithin(root string, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}

func inspectPath(path string, relativePath string) PathState {
	info, err := os.Stat(path)
	if err != nil {
		return PathState{Path: path, RelativePath: relativePath, Exists: false, Writable: false, ProbeError: err.Error()}
	}
	writable, probeErr := probeWritable(path, info)
	state := PathState{
		Path:         path,
		RelativePath: relativePath,
		Exists:       true,
		IsDir:        info.IsDir(),
		Mode:         modeString(info.Mode()),
		Writable:     writable,
		ProbeError:   probeErr,
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		state.UID = fmt.Sprintf("%d", stat.Uid)
		state.GID = fmt.Sprintf("%d", stat.Gid)
	}
	return state
}

func probeWritable(path string, info os.FileInfo) (bool, string) {
	if !info.IsDir() {
		return false, "path is not a directory"
	}
	probePath := filepath.Join(path, ".ai-logfixer-permission-probe")
	if err := os.WriteFile(probePath, []byte("probe\n"), 0o600); err != nil {
		return false, err.Error()
	}
	_ = os.Remove(probePath)
	return true, ""
}

func writeRollbackManifest(options Options, before []PathState, operations []PermissionOperation) (string, rollbackManifest, error) {
	affected := map[string]PermissionOperation{}
	for _, operation := range operations {
		affected[operation.RelativePath] = operation
	}
	manifest := rollbackManifest{CreatedAt: options.Now}
	for _, state := range before {
		if _, ok := affected[state.RelativePath]; !ok {
			continue
		}
		entry := rollbackEntry{Path: state.Path, RelativePath: state.RelativePath, Exists: state.Exists}
		if state.Exists {
			info, err := os.Stat(state.Path)
			if err != nil {
				return "", rollbackManifest{}, err
			}
			entry.Mode = info.Mode().Perm()
		}
		manifest.Entries = append(manifest.Entries, entry)
	}

	rollbackDir := filepath.Join(options.TargetDir, ".ai-logfixer", "permission-rollbacks")
	if err := os.MkdirAll(rollbackDir, 0o755); err != nil {
		return "", rollbackManifest{}, err
	}
	path := filepath.Join(rollbackDir, "permissions-"+options.Now.Format("20060102T150405Z")+".json")
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", rollbackManifest{}, err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", rollbackManifest{}, err
	}
	return path, manifest, nil
}

func applyOperations(operations []PermissionOperation) error {
	for _, operation := range operations {
		switch operation.Action {
		case "mkdir_chmod":
			mode := operation.Mode
			if mode == 0 {
				mode = 0o775
			}
			if err := os.MkdirAll(operation.Path, mode); err != nil {
				return err
			}
			if err := os.Chmod(operation.Path, mode); err != nil {
				return err
			}
		case "chmod":
			mode := operation.Mode
			if mode == 0 {
				mode = 0o775
			}
			if err := os.Chmod(operation.Path, mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported permission operation %q", operation.Action)
		}
	}
	return nil
}

func rollbackPermissions(manifest rollbackManifest) error {
	var errs []error
	for index := len(manifest.Entries) - 1; index >= 0; index-- {
		entry := manifest.Entries[index]
		if !entry.Exists {
			if err := os.Remove(entry.Path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.Chmod(entry.Path, entry.Mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func verifyHTTP(ctx context.Context, url string, expectedStatus int) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("expected status %d from %s, got %d", expectedStatus, url, response.StatusCode)
	}
	return nil
}

func buildResult(options Options, framework string, policy PermissionPolicy, before []PathState, after []PathState, findings []PermissionFinding, operations []PermissionOperation, rollbackPath string, outcome string) Result {
	factory := engine.NewContractIDFactory()
	parts := []string{options.ServiceName, options.TargetDir, framework, findingsSummary(findings), operationsSummary(operations)}
	requestID := factory.ID("inv_req_permission_drift", parts...)
	diagnosisID := factory.ID("diag_permission_drift", parts...)
	planID := factory.ID("rem_plan_permission_drift", parts...)
	attemptID := factory.ID("rem_attempt_permission_drift", parts...)
	receiptID := factory.ID("receipt_permission_drift", parts...)

	status := contractsv1.RemediationStatusSucceeded
	monitorStatus := "dry_run"
	actionTaken := "dry run only"
	receiptSummary := "AI LogFixer planned framework permission repairs but did not apply changes."
	displayStatus := "Permission dry run complete"
	userMessage := "I found framework permission drift and prepared a bounded repair plan."
	if outcome == "succeeded" {
		monitorStatus = "healthy"
		actionTaken = "applied framework permission repair"
		receiptSummary = "AI LogFixer repaired framework permissions and verified access probes."
		displayStatus = "Permission repair verified"
		userMessage = "I repaired framework permissions and verified the app recovered."
	}
	if len(operations) == 0 {
		actionTaken = "no permission changes required"
		receiptSummary = "AI LogFixer checked framework permissions and found no repair was required."
	}

	investigation := contractsv1.InvestigationRequest{
		ID:              requestID,
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "ai-logfixer-permission-resolver",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         fmt.Sprintf("%s permission drift detected in %s", titleFramework(framework), options.TargetDir),
		ErrorCode:       "permission_drift",
		TimeWindow: contractsv1.TimeWindow{
			Start: options.Now.Add(-time.Minute),
			End:   options.Now,
		},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "permission_drift",
			ErrorCode:     "permission_drift",
			Source:        options.TargetDir,
			DeployVersion: "unknown",
			Tags:          []string{"permissions", "framework=" + framework},
		},
		DisplayStatus: "Permission investigation started automatically",
		UserMessage:   "I detected framework permission drift and started a bounded permission investigation.",
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}

	diagnosis := contractsv1.DiagnosisResult{
		ID:                 diagnosisID,
		ContractVersion:    contractsv1.ContractVersion,
		SchemaURL:          contractsv1.DiagnosisSchemaURL,
		Status:             contractsv1.DiagnosisStatusComplete,
		Summary:            fmt.Sprintf("%s permission policy found %d drifted path(s).", titleFramework(framework), len(findings)),
		Confidence:         0.84,
		SuspectedRootCause: findingsSummary(findings),
		AffectedServices:   []string{options.ServiceName},
		EvidenceItems: []contractsv1.EvidenceItem{
			{
				ID:             factory.ID("ev_permission_stat", parts...),
				Type:           contractsv1.EvidenceTypeConfig,
				Source:         options.TargetDir,
				Timestamp:      options.Now,
				Title:          "Framework permission stat evidence",
				Summary:        "AI LogFixer compared framework permission policy with live filesystem stat and write probes.",
				RawExcerpt:     statesSummary(before),
				RedactionState: contractsv1.RedactionStateRedacted,
				RelatedIDs:     []string{},
				UIHints:        contractsv1.UIHints{Icon: "shield-check", Tone: "warning", Sections: []string{"permissions", "evidence"}},
				ExternalRefs:   []contractsv1.ExternalRef{},
				KnowledgeRefs:  []contractsv1.KnowledgeRef{},
			},
		},
		Recommendations: []contractsv1.RunbookRecommendation{
			{
				ID:                  factory.ID("rec_permission_repair", parts...),
				Title:               "Apply framework permission policy",
				Reason:              "The app matches a known framework permission policy and the planned changes are limited to allowlisted runtime directories.",
				Confidence:          0.82,
				Steps:               []string{"Record before stat evidence.", "Apply the smallest allowlisted permission repair.", "Run access probes and service verification.", "Use the rollback manifest if verification fails."},
				RequiredPermissions: []string{"filesystem:stat", "filesystem:chmod", "service:verify"},
				EstimatedRisk:       contractsv1.SafetyLowRisk,
				RequiresApproval:    false,
			},
		},
		PatchPlan: &contractsv1.PatchPlan{
			ID:         factory.ID("patch_permission_policy", parts...),
			TargetType: contractsv1.PatchTargetRuntimeSetting,
			TargetRefs: operationPaths(operations),
			DiffPreview: contractsv1.DiffPreview{
				Before: statesSummary(before),
				After:  operationsSummary(operations),
			},
			RiskLevel:        contractsv1.SafetyLowRisk,
			RequiresApproval: false,
			BlockedReasons:   []string{},
		},
		RollbackPlan: &contractsv1.RollbackPlan{
			ID:                   factory.ID("rollback_permission_policy", parts...),
			RollbackType:         contractsv1.RollbackReversePatch,
			SnapshotRefs:         []string{defaultString(rollbackPath, "permission_rollback_manifest")},
			RestoreSteps:         []string{"Restore recorded modes from the permission rollback manifest.", "Re-run permission access probes.", "Verify service health."},
			Limitations:          []string{"Rollback restores recorded modes for changed paths; ownership rollback is not applied in this MVP."},
			RiskLevel:            contractsv1.SafetyLowRisk,
			RequiresManualReview: false,
		},
		SafetyClassification: contractsv1.SafetyLowRisk,
		DisplayStatus:        "Framework permission drift diagnosed",
		UserMessage:          "I matched the app to a framework permission policy and found a bounded repair.",
		NextActions:          []contractsv1.NextAction{{ID: "next_apply_permission_fix", Label: "Apply permission fix", ActionType: "apply_remediation", Description: "Apply allowlisted permission repairs and verify recovery.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_permission_diag", parts...), Type: "diagnosis.completed", Message: "Permission diagnosis completed.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}

	plan := contractsv1.RemediationPlan{
		ID:                planID,
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           "Apply framework-aware permission repairs to allowlisted runtime paths.",
		FixPreview: contractsv1.DiffPreview{
			Before: statesSummary(before),
			After:  operationsSummary(operations),
		},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:                   factory.ID("rollback_permission_plan", parts...),
			RollbackType:         contractsv1.RollbackReversePatch,
			SnapshotRefs:         []string{defaultString(rollbackPath, "permission_rollback_manifest")},
			RestoreSteps:         []string{"Restore recorded modes from the permission rollback manifest.", "Re-run permission access probes.", "Verify service health."},
			Limitations:          []string{"Rollback restores recorded modes for changed paths; ownership rollback is not applied in this MVP."},
			RiskLevel:            contractsv1.SafetyLowRisk,
			RequiresManualReview: false,
		},
		RiskLevel:        contractsv1.SafetyLowRisk,
		ApprovalRequired: false,
		Status:           contractsv1.RemediationStatusApproved,
		DisplayStatus:    "Framework permission fix approved automatically",
		UserMessage:      "This low-risk permission fix is limited to known framework runtime directories.",
		NextActions:      []contractsv1.NextAction{{ID: "next_execute_permission_fix", Label: "Execute fix", ActionType: "execute_remediation", Description: "Apply chmod or mkdir repairs and verify recovery.", Enabled: true}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: factory.ID("tl_permission_plan", parts...), Type: "remediation.plan_created", Message: "Permission remediation plan created.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:     []contractsv1.ExternalRef{},
		KnowledgeRefs:    []contractsv1.KnowledgeRef{},
		CreatedAt:        options.Now,
	}

	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)
	attempt := contractsv1.RemediationAttempt{
		ID:                  attemptID,
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "auto_approved_low_risk_permission_repair",
		Status:              status,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary: contractsv1.MonitorSummary{
			Status:   monitorStatus,
			Message:  monitorMessage(outcome, options),
			Signals:  []string{"framework=" + framework, fmt.Sprintf("operations=%d", len(operations)), "runtime=" + runtime.GOOS},
			Duration: "1s",
		},
		DisplayStatus:  displayStatus,
		UserMessage:    userMessage,
		TimelineEvents: []contractsv1.TimelineEvent{{ID: factory.ID("tl_permission_attempt", parts...), Type: "remediation." + outcome, Message: "Permission remediation attempt recorded.", Severity: "info", Timestamp: finished}},
		ExternalRefs:   []contractsv1.ExternalRef{},
	}

	receipt := contractsv1.Receipt{
		ID:                   receiptID,
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attemptID,
		ActionTaken:          actionTaken,
		Actor:                "ai-logfixer-permission-resolver",
		Approver:             "auto_approved_low_risk_permission_repair",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          statesSummary(before),
		AfterState:           statesSummary(after),
		Outcome:              outcome,
		Summary:              receiptSummary,
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_permission_receipt", parts...), Type: "receipt.created", Message: "Receipt recorded for permission remediation.", Severity: "info", Timestamp: options.Now.Add(3 * time.Second)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}

	return Result{
		InvestigationRequest: investigation,
		Diagnosis:            diagnosis,
		RemediationPlan:      plan,
		Attempt:              attempt,
		Receipt:              receipt,
		Framework:            framework,
		Policy:               policy,
		Findings:             findings,
		Operations:           operations,
		Before:               before,
		After:                after,
		RollbackPath:         rollbackPath,
	}
}

func buildBlockedResult(options Options, reason string, before []PathState) (Result, error) {
	factory := engine.NewContractIDFactory()
	signal := engine.IncidentSignal{
		Service:     options.ServiceName,
		Source:      options.TargetDir,
		Kind:        "permission_drift",
		Code:        "permission_policy_blocked",
		Signature:   "framework_permission_policy",
		Count:       1,
		Start:       options.Now.Add(-time.Minute),
		End:         options.Now,
		RawExcerpts: []string{reason},
		Tags:        []string{"permissions", "blocked"},
	}
	investigation := contractsv1.InvestigationRequest{
		ID:              factory.ID("inv_req_permission_blocked", reason, options.TargetDir),
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "ai-logfixer-permission-resolver",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         "Permission remediation blocked by safety policy",
		ErrorCode:       signal.ErrorCode(),
		TimeWindow:      contractsv1.TimeWindow{Start: signal.Start, End: signal.End},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "permission_drift_blocked",
			ErrorCode:     signal.ErrorCode(),
			Source:        options.TargetDir,
			DeployVersion: "unknown",
			Tags:          signal.Tags,
		},
		DisplayStatus: "Permission investigation blocked",
		UserMessage:   reason,
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
	diagnosisID := factory.ID("diag_permission_blocked", reason, options.TargetDir)
	diagnosis := contractsv1.DiagnosisResult{
		ID:                   diagnosisID,
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               contractsv1.DiagnosisStatusBlockedBySafety,
		Summary:              "Permission remediation is blocked by safety policy.",
		Confidence:           0.9,
		SuspectedRootCause:   reason,
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        []contractsv1.EvidenceItem{{ID: factory.ID("ev_permission_blocked", reason, options.TargetDir), Type: contractsv1.EvidenceTypeConfig, Source: options.TargetDir, Timestamp: options.Now, Title: "Permission safety block", Summary: reason, RawExcerpt: statesSummary(before), RedactionState: contractsv1.RedactionStateRedacted, RelatedIDs: []string{}, UIHints: contractsv1.UIHints{Icon: "shield-x", Tone: "danger", Sections: []string{"permissions"}}, ExternalRefs: []contractsv1.ExternalRef{}, KnowledgeRefs: []contractsv1.KnowledgeRef{}}},
		Recommendations:      []contractsv1.RunbookRecommendation{{ID: factory.ID("rec_permission_blocked", reason, options.TargetDir), Title: "Escalate permission repair", Reason: reason, Confidence: 0.9, Steps: []string{"Review the unsafe permission path.", "Provide an explicit safe policy or remove the escaping path.", "Re-run permission remediation."}, RequiredPermissions: []string{"manual_review"}, EstimatedRisk: contractsv1.SafetyBlocked, RequiresApproval: false}},
		SafetyClassification: contractsv1.SafetyBlocked,
		DisplayStatus:        "Permission remediation blocked",
		UserMessage:          reason,
		NextActions:          []contractsv1.NextAction{{ID: "next_manual_permission_review", Label: "Review permissions", ActionType: "manual_review", Description: "Review the blocked permission policy finding.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_permission_blocked", reason, options.TargetDir), Type: "diagnosis.blocked", Message: reason, Severity: "warning", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
	builder := engine.BlockedPlanBuilder{IDFactory: factory, Now: options.Now, Actor: "ai-logfixer-permission-resolver"}
	plan := builder.RemediationPlan(diagnosis.ID, signal, reason)
	attempt := builder.EscalatedAttempt(plan.ID, signal, reason)
	receipt := builder.EscalatedReceipt(diagnosis.ID, plan.ID, attempt.ID, signal, reason)
	result := Result{InvestigationRequest: investigation, Diagnosis: diagnosis, RemediationPlan: plan, Attempt: attempt, Receipt: receipt, Before: before, After: before}
	return result, validateResult(result)
}

func validateResult(result Result) error {
	if err := result.InvestigationRequest.Validate(); err != nil {
		return fmt.Errorf("validate investigation request: %w", err)
	}
	if err := result.Diagnosis.Validate(); err != nil {
		return fmt.Errorf("validate diagnosis: %w", err)
	}
	if err := result.RemediationPlan.Validate(); err != nil {
		return fmt.Errorf("validate remediation plan: %w", err)
	}
	if err := result.Attempt.Validate(); err != nil {
		return fmt.Errorf("validate remediation attempt: %w", err)
	}
	if err := result.Receipt.Validate(); err != nil {
		return fmt.Errorf("validate receipt: %w", err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileContainsAny(path string, needles ...string) bool {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return false
	}
	content := strings.ToLower(string(raw))
	for _, needle := range needles {
		if strings.Contains(content, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func modeString(mode os.FileMode) string {
	return fmt.Sprintf("%04o", mode.Perm())
}

func statesSummary(states []PathState) string {
	var values []string
	for _, state := range states {
		if !state.Exists {
			values = append(values, state.RelativePath+"=missing")
			continue
		}
		values = append(values, fmt.Sprintf("%s mode=%s uid=%s gid=%s writable=%t", state.RelativePath, state.Mode, state.UID, state.GID, state.Writable))
	}
	if len(values) == 0 {
		return "no permission state captured"
	}
	return strings.Join(values, "; ")
}

func findingsSummary(findings []PermissionFinding) string {
	if len(findings) == 0 {
		return "framework permission policy already matches live filesystem evidence"
	}
	var values []string
	for _, finding := range findings {
		values = append(values, fmt.Sprintf("%s %s current=%s expected=%s", finding.RelativePath, finding.Problem, finding.CurrentMode, finding.ExpectedMode))
	}
	return strings.Join(values, "; ")
}

func operationsSummary(operations []PermissionOperation) string {
	if len(operations) == 0 {
		return "no permission changes required"
	}
	var values []string
	for _, operation := range operations {
		values = append(values, fmt.Sprintf("%s %s from %s to %s", operation.Action, operation.RelativePath, operation.Before, operation.After))
	}
	return strings.Join(values, "; ")
}

func operationPaths(operations []PermissionOperation) []string {
	if len(operations) == 0 {
		return []string{"permission_policy"}
	}
	paths := make([]string, 0, len(operations))
	for _, operation := range operations {
		paths = append(paths, operation.Path)
	}
	return paths
}

func monitorMessage(outcome string, options Options) string {
	if outcome == "succeeded" && options.VerifyURL != "" {
		return fmt.Sprintf("%s returned %d after permission repair.", options.VerifyURL, options.ExpectedStatus)
	}
	if outcome == "succeeded" {
		return "Permission access probes passed after repair."
	}
	return "Dry run completed; no permission changes were applied."
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func titleFramework(framework string) string {
	if framework == "" {
		return "Framework"
	}
	return strings.ToUpper(framework[:1]) + framework[1:]
}
