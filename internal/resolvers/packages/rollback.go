package packages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	VerificationNotRun    VerificationStatus = "not_run"
	VerificationSucceeded VerificationStatus = "succeeded"
	VerificationFailed    VerificationStatus = "failed"
)

var dependencySections = []string{
	"dependencies",
	"devDependencies",
	"optionalDependencies",
	"peerDependencies",
}

type VerificationStatus string

type Options struct {
	PackageFile    string
	PackageName    string
	CurrentSpec    string
	KnownGoodSpec  string
	VerifyCommand  string
	VerifyURL      string
	ExpectedStatus int
	WorkingDir     string
	CommandTimeout time.Duration
	Now            time.Time
	HTTPClient     *http.Client
}

type Result struct {
	PackageFile       string             `json:"package_file"`
	PackageName       string             `json:"package_name"`
	DependencySection string             `json:"dependency_section"`
	Before            PackageSpec        `json:"before"`
	After             PackageSpec        `json:"after"`
	Rollback          RollbackData       `json:"rollback"`
	Verification      VerificationResult `json:"verification"`
	Applied           bool               `json:"applied"`
	RolledBack        bool               `json:"rolled_back"`
}

type PackageSpec struct {
	Spec string `json:"spec"`
}

type RollbackData struct {
	BackupDir    string `json:"backup_dir"`
	BackupPath   string `json:"backup_path"`
	ManifestPath string `json:"manifest_path"`
}

type RollbackManifest struct {
	PackageFile       string    `json:"package_file"`
	PackageName       string    `json:"package_name"`
	DependencySection string    `json:"dependency_section"`
	BeforeSpec        string    `json:"before_spec"`
	AfterSpec         string    `json:"after_spec"`
	BackupPath        string    `json:"backup_path"`
	CreatedAt         time.Time `json:"created_at"`
}

type VerificationResult struct {
	Kind           string             `json:"kind"`
	Status         VerificationStatus `json:"status"`
	Command        string             `json:"command,omitempty"`
	URL            string             `json:"url,omitempty"`
	ExpectedStatus int                `json:"expected_status,omitempty"`
	HTTPStatus     int                `json:"http_status,omitempty"`
	Stdout         string             `json:"stdout,omitempty"`
	Stderr         string             `json:"stderr,omitempty"`
	ExitCode       int                `json:"exit_code,omitempty"`
}

type packageDependency struct {
	section string
	spec    string
}

func Rollback(ctx context.Context, options Options) (Result, error) {
	options = normalizeOptions(options)
	if err := validateOptions(options); err != nil {
		return Result{}, err
	}

	packagePath, err := filepath.Abs(filepath.Clean(options.PackageFile))
	if err != nil {
		return Result{}, fmt.Errorf("resolve package file: %w", err)
	}
	options.PackageFile = packagePath
	if options.WorkingDir == "" {
		options.WorkingDir = filepath.Dir(packagePath)
	}

	raw, document, err := readPackageFile(packagePath)
	if err != nil {
		return Result{}, fmt.Errorf("read package file: %w", err)
	}
	dependency, err := findDependency(document, options.PackageName)
	if err != nil {
		return Result{}, err
	}
	if dependency.spec != options.CurrentSpec {
		return Result{}, fmt.Errorf("current spec mismatch for %s: package file has %q, policy expected %q", options.PackageName, dependency.spec, options.CurrentSpec)
	}

	result := Result{
		PackageFile:       packagePath,
		PackageName:       options.PackageName,
		DependencySection: dependency.section,
		Before:            PackageSpec{Spec: dependency.spec},
		After:             PackageSpec{Spec: options.KnownGoodSpec},
		Verification:      VerificationResult{Status: VerificationNotRun},
	}

	info, err := os.Stat(packagePath)
	if err != nil {
		return result, fmt.Errorf("stat package file: %w", err)
	}
	rollback, err := writeRollbackData(packagePath, options, dependency.section, raw, info.Mode().Perm())
	if err != nil {
		return result, fmt.Errorf("write rollback data: %w", err)
	}
	result.Rollback = rollback

	if err := setDependencySpec(document, dependency.section, options.PackageName, options.KnownGoodSpec); err != nil {
		return result, err
	}
	if err := writePackageFile(packagePath, document, info.Mode().Perm()); err != nil {
		return result, fmt.Errorf("write package rollback: %w", err)
	}
	result.Applied = true

	verification, err := verify(ctx, options)
	result.Verification = verification
	if err != nil {
		if rollbackErr := restorePackageFile(packagePath, rollback.BackupPath, info.Mode().Perm()); rollbackErr == nil {
			result.RolledBack = true
		} else {
			err = errors.Join(err, fmt.Errorf("restore package file: %w", rollbackErr))
		}
		return result, fmt.Errorf("verify package rollback: %w", err)
	}

	return result, nil
}

func normalizeOptions(options Options) Options {
	options.PackageFile = strings.TrimSpace(options.PackageFile)
	options.PackageName = strings.TrimSpace(options.PackageName)
	options.CurrentSpec = strings.TrimSpace(options.CurrentSpec)
	options.KnownGoodSpec = strings.TrimSpace(options.KnownGoodSpec)
	options.VerifyCommand = strings.TrimSpace(options.VerifyCommand)
	options.VerifyURL = strings.TrimSpace(options.VerifyURL)
	options.WorkingDir = strings.TrimSpace(options.WorkingDir)
	if options.ExpectedStatus == 0 {
		options.ExpectedStatus = http.StatusOK
	}
	if options.CommandTimeout == 0 {
		options.CommandTimeout = 30 * time.Second
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	return options
}

func validateOptions(options Options) error {
	if options.PackageFile == "" {
		return errors.New("package file is required")
	}
	if options.PackageName == "" {
		return errors.New("package name is required")
	}
	if options.CurrentSpec == "" {
		return errors.New("current broken package spec is required")
	}
	if options.KnownGoodSpec == "" {
		return errors.New("known-good package spec is required")
	}
	if options.CurrentSpec == options.KnownGoodSpec {
		return errors.New("current and known-good package specs must differ")
	}
	if options.VerifyCommand == "" && options.VerifyURL == "" {
		return errors.New("verify command or verify URL is required")
	}
	if options.VerifyCommand != "" && options.VerifyURL != "" {
		return errors.New("provide only one verification mode: command or URL")
	}
	return nil
}

func readPackageFile(path string) ([]byte, map[string]any, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, nil, err
	}
	return raw, document, nil
}

func findDependency(document map[string]any, packageName string) (packageDependency, error) {
	var found []packageDependency
	for _, section := range dependencySections {
		rawSection, ok := document[section]
		if !ok {
			continue
		}
		dependencies, ok := rawSection.(map[string]any)
		if !ok {
			continue
		}
		rawSpec, ok := dependencies[packageName]
		if !ok {
			continue
		}
		spec, ok := rawSpec.(string)
		if !ok {
			return packageDependency{}, fmt.Errorf("%s entry for %s must be a string", section, packageName)
		}
		found = append(found, packageDependency{section: section, spec: spec})
	}
	if len(found) == 0 {
		return packageDependency{}, fmt.Errorf("package %s was not found in supported dependency sections", packageName)
	}
	if len(found) > 1 {
		sections := make([]string, 0, len(found))
		for _, dependency := range found {
			sections = append(sections, dependency.section)
		}
		return packageDependency{}, fmt.Errorf("package %s appears in multiple dependency sections: %s", packageName, strings.Join(sections, ", "))
	}
	return found[0], nil
}

func setDependencySpec(document map[string]any, section string, packageName string, spec string) error {
	rawSection, ok := document[section]
	if !ok {
		return fmt.Errorf("dependency section %s disappeared before patch", section)
	}
	dependencies, ok := rawSection.(map[string]any)
	if !ok {
		return fmt.Errorf("dependency section %s must be an object", section)
	}
	dependencies[packageName] = spec
	return nil
}

func writeRollbackData(path string, options Options, section string, rawPackage []byte, mode os.FileMode) (RollbackData, error) {
	backupDir := filepath.Join(filepath.Dir(path), ".ai-logfixer-backups", "package-rollback-"+options.Now.Format("20060102T150405Z")+"-"+safePathFragment(options.PackageName))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return RollbackData{}, fmt.Errorf("create package rollback directory: %w", err)
	}
	backupPath := filepath.Join(backupDir, filepath.Base(path))
	if err := os.WriteFile(backupPath, rawPackage, mode); err != nil {
		return RollbackData{}, fmt.Errorf("write package backup: %w", err)
	}

	manifestPath := filepath.Join(backupDir, "rollback-manifest.json")
	manifest := RollbackManifest{
		PackageFile:       path,
		PackageName:       options.PackageName,
		DependencySection: section,
		BeforeSpec:        options.CurrentSpec,
		AfterSpec:         options.KnownGoodSpec,
		BackupPath:        backupPath,
		CreatedAt:         options.Now,
	}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return RollbackData{}, fmt.Errorf("encode rollback manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, append(manifestRaw, '\n'), 0o644); err != nil {
		return RollbackData{}, fmt.Errorf("write rollback manifest: %w", err)
	}

	return RollbackData{
		BackupDir:    backupDir,
		BackupPath:   backupPath,
		ManifestPath: manifestPath,
	}, nil
}

func safePathFragment(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	fragment := strings.Trim(builder.String(), "-")
	if fragment == "" {
		return "package"
	}
	return fragment
}

func writePackageFile(path string, document map[string]any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode package file: %w", err)
	}
	return os.WriteFile(path, append(raw, '\n'), mode)
}

func restorePackageFile(path string, backupPath string, mode os.FileMode) error {
	input, err := os.Open(filepath.Clean(backupPath))
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer output.Close()

	_, err = io.Copy(output, input)
	return err
}

func verify(ctx context.Context, options Options) (VerificationResult, error) {
	if options.VerifyCommand != "" {
		return verifyCommand(ctx, options)
	}
	return verifyURL(ctx, options)
}

func verifyCommand(ctx context.Context, options Options) (VerificationResult, error) {
	result := VerificationResult{
		Kind:    "command",
		Status:  VerificationFailed,
		Command: options.VerifyCommand,
	}
	if options.CommandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.CommandTimeout)
		defer cancel()
	}

	name, args := shellCommand(options.VerifyCommand)
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = options.WorkingDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("command timed out after %s: %s", options.CommandTimeout, options.VerifyCommand)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		return result, fmt.Errorf("command failed: %s: %w", options.VerifyCommand, err)
	}
	result.Status = VerificationSucceeded
	return result, nil
}

func verifyURL(ctx context.Context, options Options) (VerificationResult, error) {
	result := VerificationResult{
		Kind:           "http",
		Status:         VerificationFailed,
		URL:            options.VerifyURL,
		ExpectedStatus: options.ExpectedStatus,
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: options.CommandTimeout}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, options.VerifyURL, nil)
	if err != nil {
		return result, err
	}
	response, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()

	result.HTTPStatus = response.StatusCode
	if response.StatusCode != options.ExpectedStatus {
		return result, fmt.Errorf("expected HTTP status %d, got %d", options.ExpectedStatus, response.StatusCode)
	}
	result.Status = VerificationSucceeded
	return result, nil
}

func shellCommand(commandLine string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", commandLine}
	}
	return "/bin/sh", []string{"-c", commandLine}
}
