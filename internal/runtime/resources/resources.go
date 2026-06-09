package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const manifestVersion = "ai-logfixer-runtime-resource-v1"

type Kind string

const (
	KindDirectory Kind = "dir"
	KindFile      Kind = "file"
)

type ContentStrategy string

const (
	ContentEmpty   ContentStrategy = "empty"
	ContentLiteral ContentStrategy = "literal"
)

type Options struct {
	AppRoot          string
	ResourcePath     string
	Allowlist        []string
	Kind             Kind
	Mode             os.FileMode
	ContentStrategy  ContentStrategy
	Content          string
	VerifyURL        string
	ExpectedStatus   int
	VerifyCommand    string
	Now              time.Time
	ManifestBaseName string
	HTTPClient       *http.Client
}

type Result struct {
	AppRoot       string               `json:"app_root"`
	RequestedPath string               `json:"requested_path"`
	RelativePath  string               `json:"relative_path"`
	ResolvedPath  string               `json:"resolved_path"`
	Kind          Kind                 `json:"kind"`
	Before        ResourceState        `json:"before"`
	After         ResourceState        `json:"after"`
	CreatedPaths  []string             `json:"created_paths"`
	ManifestPath  string               `json:"manifest_path"`
	Applied       bool                 `json:"applied"`
	Verified      bool                 `json:"verified"`
	Verification  []VerificationResult `json:"verification"`
	Rollback      RollbackEvidence     `json:"rollback"`
}

type ResourceState struct {
	Path          string `json:"path"`
	RelativePath  string `json:"relative_path"`
	Exists        bool   `json:"exists"`
	Kind          Kind   `json:"kind,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Size          int64  `json:"size,omitempty"`
	SymlinkTarget string `json:"symlink_target,omitempty"`
}

type VerificationResult struct {
	Type           string `json:"type"`
	URL            string `json:"url,omitempty"`
	ExpectedStatus int    `json:"expected_status,omitempty"`
	ActualStatus   int    `json:"actual_status,omitempty"`
	Command        string `json:"command,omitempty"`
	Stdout         string `json:"stdout,omitempty"`
	Stderr         string `json:"stderr,omitempty"`
	ExitCode       int    `json:"exit_code,omitempty"`
	Passed         bool   `json:"passed"`
}

type RollbackEvidence struct {
	ManifestPath string   `json:"manifest_path"`
	RolledBack   bool     `json:"rolled_back"`
	RemovedPaths []string `json:"removed_paths"`
	Error        string   `json:"error,omitempty"`
}

type Manifest struct {
	Version   string          `json:"version"`
	AppRoot   string          `json:"app_root"`
	CreatedAt time.Time       `json:"created_at"`
	Entries   []ManifestEntry `json:"entries"`
}

type ManifestEntry struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
	Mode string `json:"mode"`
}

type plannedCreation struct {
	relPath string
	kind    Kind
	mode    os.FileMode
}

func Ensure(ctx context.Context, options Options) (Result, error) {
	options = normalizeOptions(options)

	root, err := resolveAppRoot(options.AppRoot)
	if err != nil {
		return Result{}, err
	}
	relPath, resolvedPath, err := resolveRequestedPath(root, options.ResourcePath)
	if err != nil {
		return Result{}, err
	}
	if err := requireAllowlisted(root, relPath, options.Allowlist); err != nil {
		return Result{}, err
	}
	if err := ensureNoSymlinkEscape(root, resolvedPath); err != nil {
		return Result{}, err
	}

	before, err := stateFor(root, relPath)
	if err != nil {
		return Result{}, err
	}
	if before.Exists && before.Kind != options.Kind {
		return Result{}, fmt.Errorf("resource %s exists as %s, expected %s", relPath, before.Kind, options.Kind)
	}

	result := Result{
		AppRoot:       root,
		RequestedPath: options.ResourcePath,
		RelativePath:  relPath,
		ResolvedPath:  resolvedPath,
		Kind:          options.Kind,
		Before:        before,
		After:         before,
	}

	creations, err := planCreations(root, relPath, options.Kind, options.Mode)
	if err != nil {
		return result, err
	}
	if len(creations) == 0 {
		verification, err := verify(ctx, root, options)
		result.Verification = verification
		if err != nil {
			return result, fmt.Errorf("verify resource: %w", err)
		}
		result.Verified = true
		return result, nil
	}
	if !hasVerification(options) {
		return result, errors.New("verification URL or command is required before creating runtime resources")
	}

	for _, creation := range creations {
		result.CreatedPaths = append(result.CreatedPaths, creation.relPath)
	}

	manifestPath, err := writeRollbackManifest(root, creations, options.Now, options.ManifestBaseName)
	if err != nil {
		return result, fmt.Errorf("write rollback manifest: %w", err)
	}
	result.ManifestPath = manifestPath
	result.Rollback.ManifestPath = manifestPath

	if err := applyCreations(root, creations, options); err != nil {
		result.Rollback = rollbackAfterFailure(manifestPath)
		result.After, _ = stateFor(root, relPath)
		return result, fmt.Errorf("create runtime resource: %w", err)
	}
	result.Applied = true

	verification, err := verify(ctx, root, options)
	result.Verification = verification
	if err != nil {
		result.Rollback = rollbackAfterFailure(manifestPath)
		result.After, _ = stateFor(root, relPath)
		return result, fmt.Errorf("verify resource: %w", err)
	}

	result.Verified = true
	result.After, err = stateFor(root, relPath)
	if err != nil {
		return result, err
	}
	return result, nil
}

func Rollback(manifestPath string) (RollbackEvidence, error) {
	raw, err := os.ReadFile(filepath.Clean(manifestPath))
	if err != nil {
		return RollbackEvidence{ManifestPath: manifestPath, Error: err.Error()}, fmt.Errorf("read rollback manifest: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return RollbackEvidence{ManifestPath: manifestPath, Error: err.Error()}, fmt.Errorf("decode rollback manifest: %w", err)
	}
	if manifest.Version != manifestVersion {
		err := fmt.Errorf("unsupported rollback manifest version %q", manifest.Version)
		return RollbackEvidence{ManifestPath: manifestPath, Error: err.Error()}, err
	}

	root, err := resolveAppRoot(manifest.AppRoot)
	if err != nil {
		return RollbackEvidence{ManifestPath: manifestPath, Error: err.Error()}, err
	}

	evidence := RollbackEvidence{ManifestPath: manifestPath}
	for index := len(manifest.Entries) - 1; index >= 0; index-- {
		entry := manifest.Entries[index]
		if pathHasTraversal(entry.Path) {
			err := fmt.Errorf("rollback path traversal blocked for %s", entry.Path)
			evidence.Error = err.Error()
			return evidence, err
		}
		absPath := filepath.Join(root, filepath.FromSlash(entry.Path))
		if !lexicallyInsideRoot(root, absPath) {
			err := fmt.Errorf("rollback path %s is outside app root", entry.Path)
			evidence.Error = err.Error()
			return evidence, err
		}
		if err := removeCreated(absPath, entry.Kind); err != nil {
			evidence.Error = err.Error()
			return evidence, err
		}
		evidence.RemovedPaths = append(evidence.RemovedPaths, entry.Path)
	}
	evidence.RolledBack = true
	return evidence, nil
}

func normalizeOptions(options Options) Options {
	if options.Kind == "" {
		options.Kind = KindDirectory
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if options.ExpectedStatus == 0 {
		options.ExpectedStatus = http.StatusOK
	}
	if options.Mode == 0 {
		if options.Kind == KindFile {
			options.Mode = 0o644
		} else {
			options.Mode = 0o755
		}
	}
	if options.ContentStrategy == "" {
		options.ContentStrategy = ContentEmpty
	}
	return options
}

func resolveAppRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("app root is required")
	}
	absRoot, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve app root: %w", err)
	}
	root, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve app root symlinks: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat app root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("app root %s is not a directory", root)
	}
	return root, nil
}

func resolveRequestedPath(root string, rawPath string) (string, string, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", "", errors.New("resource path is required")
	}
	if pathHasTraversal(rawPath) {
		return "", "", fmt.Errorf("path traversal blocked for %s", rawPath)
	}

	candidate := rawPath
	if filepath.IsAbs(rawPath) {
		candidate = filepath.Clean(rawPath)
	} else {
		candidate = filepath.Join(root, filepath.Clean(rawPath))
	}
	if !lexicallyInsideRoot(root, candidate) {
		return "", "", fmt.Errorf("resource path %s is outside app root", rawPath)
	}

	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve resource path: %w", err)
	}
	if rel == "." {
		return "", "", errors.New("resource path must identify a child of app root")
	}
	return filepath.ToSlash(rel), filepath.Join(root, rel), nil
}

func requireAllowlisted(root string, relPath string, allowlist []string) error {
	if len(allowlist) == 0 {
		return errors.New("at least one allowlisted resource path is required")
	}
	for _, entry := range allowlist {
		allowRel, err := normalizeAllowlistEntry(root, entry)
		if err != nil {
			return err
		}
		if allowRel == relPath {
			return nil
		}
	}
	return fmt.Errorf("resource path %s is not allowlisted", relPath)
}

func normalizeAllowlistEntry(root string, entry string) (string, error) {
	if strings.TrimSpace(entry) == "" {
		return "", errors.New("allowlisted resource path is empty")
	}
	if pathHasTraversal(entry) {
		return "", fmt.Errorf("path traversal blocked for allowlisted resource %s", entry)
	}

	candidate := entry
	if filepath.IsAbs(entry) {
		candidate = filepath.Clean(entry)
	} else {
		candidate = filepath.Join(root, filepath.Clean(entry))
	}
	if !lexicallyInsideRoot(root, candidate) {
		return "", fmt.Errorf("allowlisted resource path %s is outside app root", entry)
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve allowlisted resource path: %w", err)
	}
	if rel == "." {
		return "", errors.New("allowlisted resource path must identify a child of app root")
	}
	return filepath.ToSlash(rel), nil
}

func ensureNoSymlinkEscape(root string, targetPath string) error {
	current := filepath.Clean(targetPath)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			resolved := current
			if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || current == targetPath {
				resolvedPath, err := filepath.EvalSymlinks(current)
				if err != nil {
					return fmt.Errorf("resolve symlink path %s: %w", current, err)
				}
				resolved = resolvedPath
			}
			if !lexicallyInsideRoot(root, resolved) {
				return fmt.Errorf("symlink escape blocked for %s", targetPath)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect resource path %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("resource path %s has no existing ancestor", targetPath)
		}
		current = parent
	}
}

func planCreations(root string, relPath string, kind Kind, mode os.FileMode) ([]plannedCreation, error) {
	if kind != KindDirectory && kind != KindFile {
		return nil, fmt.Errorf("unsupported resource kind %q", kind)
	}

	segments := strings.Split(relPath, "/")
	var creations []plannedCreation
	current := root
	lastDirectoryIndex := len(segments)
	if kind == KindFile {
		lastDirectoryIndex = len(segments) - 1
	}

	for index := 0; index < lastDirectoryIndex; index++ {
		current = filepath.Join(current, segments[index])
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, err := filepath.EvalSymlinks(current)
				if err != nil {
					return nil, fmt.Errorf("resolve symlink path %s: %w", current, err)
				}
				if !lexicallyInsideRoot(root, resolved) {
					return nil, fmt.Errorf("symlink escape blocked for %s", current)
				}
				info, err = os.Stat(current)
				if err != nil {
					return nil, fmt.Errorf("stat symlink path %s: %w", current, err)
				}
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("resource parent %s is not a directory", current)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect resource parent %s: %w", current, err)
		}
		creations = append(creations, plannedCreation{
			relPath: filepath.ToSlash(strings.Join(segments[:index+1], "/")),
			kind:    KindDirectory,
			mode:    modeForDirectory(kind, mode),
		})
	}

	if kind == KindFile {
		targetPath := filepath.Join(root, filepath.FromSlash(relPath))
		info, err := os.Lstat(targetPath)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, err := filepath.EvalSymlinks(targetPath)
				if err != nil {
					return nil, fmt.Errorf("resolve symlink path %s: %w", targetPath, err)
				}
				if !lexicallyInsideRoot(root, resolved) {
					return nil, fmt.Errorf("symlink escape blocked for %s", targetPath)
				}
				info, err = os.Stat(targetPath)
				if err != nil {
					return nil, fmt.Errorf("stat symlink path %s: %w", targetPath, err)
				}
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("resource %s is not a regular file", relPath)
			}
			return creations, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect resource file %s: %w", targetPath, err)
		}
		creations = append(creations, plannedCreation{relPath: relPath, kind: KindFile, mode: mode})
	}
	return creations, nil
}

func modeForDirectory(kind Kind, mode os.FileMode) os.FileMode {
	if kind == KindDirectory {
		return mode
	}
	return 0o755
}

func writeRollbackManifest(root string, creations []plannedCreation, now time.Time, baseName string) (string, error) {
	if baseName == "" {
		baseName = "resource"
	}
	backupDir := filepath.Join(root, ".ai-logfixer-backups", baseName+"-"+now.Format("20060102T150405Z"))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}

	manifest := Manifest{
		Version:   manifestVersion,
		AppRoot:   root,
		CreatedAt: now,
		Entries:   make([]ManifestEntry, 0, len(creations)),
	}
	for _, creation := range creations {
		manifest.Entries = append(manifest.Entries, ManifestEntry{
			Path: creation.relPath,
			Kind: creation.kind,
			Mode: formatMode(creation.mode),
		})
	}

	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	manifestPath := filepath.Join(backupDir, "rollback-manifest.json")
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		return "", err
	}
	return manifestPath, nil
}

func applyCreations(root string, creations []plannedCreation, options Options) error {
	for _, creation := range creations {
		path := filepath.Join(root, filepath.FromSlash(creation.relPath))
		switch creation.kind {
		case KindDirectory:
			if err := os.Mkdir(path, creation.mode); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		case KindFile:
			if err := os.WriteFile(path, placeholderContent(options), creation.mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported creation kind %q", creation.kind)
		}
	}
	return nil
}

func placeholderContent(options Options) []byte {
	if options.ContentStrategy == ContentLiteral {
		return []byte(options.Content)
	}
	return []byte{}
}

func verify(ctx context.Context, root string, options Options) ([]VerificationResult, error) {
	if !hasVerification(options) {
		return nil, errors.New("verification URL or command is required")
	}

	var results []VerificationResult
	if strings.TrimSpace(options.VerifyURL) != "" {
		result, err := verifyURL(ctx, options)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	if strings.TrimSpace(options.VerifyCommand) != "" {
		result, err := verifyCommand(ctx, root, options.VerifyCommand)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func verifyURL(ctx context.Context, options Options) (VerificationResult, error) {
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, options.VerifyURL, nil)
	if err != nil {
		return VerificationResult{Type: "url", URL: options.VerifyURL, ExpectedStatus: options.ExpectedStatus}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return VerificationResult{Type: "url", URL: options.VerifyURL, ExpectedStatus: options.ExpectedStatus}, err
	}
	defer response.Body.Close()

	result := VerificationResult{
		Type:           "url",
		URL:            options.VerifyURL,
		ExpectedStatus: options.ExpectedStatus,
		ActualStatus:   response.StatusCode,
		Passed:         response.StatusCode == options.ExpectedStatus,
	}
	if !result.Passed {
		return result, fmt.Errorf("expected status %d from %s, got %d", options.ExpectedStatus, options.VerifyURL, response.StatusCode)
	}
	return result, nil
}

func verifyCommand(ctx context.Context, root string, command string) (VerificationResult, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Dir = root

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := VerificationResult{
		Type:     "command",
		Command:  command,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
		Passed:   err == nil,
	}
	if err != nil {
		return result, fmt.Errorf("verification command exited with status %d", result.ExitCode)
	}
	return result, nil
}

func stateFor(root string, relPath string) (ResourceState, error) {
	path := filepath.Join(root, filepath.FromSlash(relPath))
	state := ResourceState{
		Path:         path,
		RelativePath: relPath,
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, fmt.Errorf("stat resource %s: %w", relPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return state, fmt.Errorf("resolve symlink resource %s: %w", relPath, err)
		}
		state.SymlinkTarget = target
		info, err = os.Stat(path)
		if err != nil {
			return state, fmt.Errorf("stat symlink resource %s: %w", relPath, err)
		}
	}
	state.Exists = true
	state.Mode = formatMode(info.Mode().Perm())
	state.Size = info.Size()
	if info.IsDir() {
		state.Kind = KindDirectory
	} else if info.Mode().IsRegular() {
		state.Kind = KindFile
	}
	return state, nil
}

func rollbackAfterFailure(manifestPath string) RollbackEvidence {
	evidence, err := Rollback(manifestPath)
	if err != nil {
		evidence.Error = err.Error()
	}
	return evidence
}

func removeCreated(path string, kind Kind) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect created path %s: %w", path, err)
	}
	if kind == KindFile {
		if info.IsDir() {
			return fmt.Errorf("refusing to remove directory %s as file rollback", path)
		}
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

func hasVerification(options Options) bool {
	return strings.TrimSpace(options.VerifyURL) != "" || strings.TrimSpace(options.VerifyCommand) != ""
}

func lexicallyInsideRoot(root string, candidate string) bool {
	cleanRoot := filepath.Clean(root)
	cleanCandidate := filepath.Clean(candidate)
	rel, err := filepath.Rel(cleanRoot, cleanCandidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathHasTraversal(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func formatMode(mode os.FileMode) string {
	return fmt.Sprintf("%04o", mode.Perm())
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 1
}
