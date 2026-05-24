package agentfix

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	ChangeCreate = "create"
	ChangeModify = "modify"
	ChangeDelete = "delete"
)

type Options struct {
	TargetDir          string
	Prompt             string
	AgentCommand       string
	AgentModel         string
	AgentName          string
	ValidationCommands []string
	ExcludePaths       []string
	Apply              bool
	Now                time.Time
	KeepWorkdir        bool
	MaxChangedFiles    int
	AutoPHPLint        bool
	AgentRunner        AgentRunner
}

type Result struct {
	StagingDir        string             `json:"staging_dir"`
	PromptPath        string             `json:"prompt_path"`
	BackupDir         string             `json:"backup_dir"`
	ManifestPath      string             `json:"manifest_path"`
	AgentCommand      []string           `json:"agent_command"`
	AgentOutput       CommandOutput      `json:"agent_output"`
	ValidationResults []ValidationResult `json:"validation_results"`
	Changes           []Change           `json:"changes"`
	Applied           bool               `json:"applied"`
	ValidationPassed  bool               `json:"validation_passed"`
	RollbackAvailable bool               `json:"rollback_available"`
	RollbackCommand   string             `json:"rollback_command"`
}

type AgentContext struct {
	TargetDir  string
	StagingDir string
	Prompt     string
	PromptPath string
	Command    []string
}

type AgentRunner func(context.Context, AgentContext) (CommandOutput, error)

type CommandOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

type ValidationResult struct {
	Command  string `json:"command"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Passed   bool   `json:"passed"`
}

type Change struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type Manifest struct {
	TargetDir string           `json:"target_dir"`
	BackupDir string           `json:"backup_dir"`
	CreatedAt time.Time        `json:"created_at"`
	Changes   []ManifestChange `json:"changes"`
}

type ManifestChange struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	BackupPath string `json:"backup_path"`
}

type fileSnapshot struct {
	hash string
	size int64
	mode os.FileMode
}

func Run(ctx context.Context, options Options) (Result, error) {
	if options.TargetDir == "" {
		return Result{}, errors.New("target directory is required")
	}
	if strings.TrimSpace(options.Prompt) == "" {
		return Result{}, errors.New("prompt is required")
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if options.MaxChangedFiles == 0 {
		options.MaxChangedFiles = 50
	}

	targetDir, err := filepath.Abs(filepath.Clean(options.TargetDir))
	if err != nil {
		return Result{}, fmt.Errorf("resolve target directory: %w", err)
	}
	options.TargetDir = targetDir

	workRoot, err := os.MkdirTemp("", "ai-logfixer-agent-*")
	if err != nil {
		return Result{}, fmt.Errorf("create external agent workdir: %w", err)
	}
	if !options.KeepWorkdir {
		defer os.RemoveAll(workRoot)
	}

	stagingDir := filepath.Join(workRoot, "staging")
	matcher := newExcludeMatcher(options.ExcludePaths)
	if err := copyDir(targetDir, stagingDir, matcher); err != nil {
		return Result{}, fmt.Errorf("create staging copy: %w", err)
	}

	prompt := buildPrompt(options.Prompt)
	promptPath := filepath.Join(workRoot, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return Result{}, fmt.Errorf("write prompt bundle: %w", err)
	}

	command, err := buildAgentCommand(options, targetDir, stagingDir, promptPath)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		StagingDir:   stagingDir,
		PromptPath:   promptPath,
		AgentCommand: command,
	}

	runner := options.AgentRunner
	if runner == nil {
		runner = runAgentCommand
	}
	output, err := runner(ctx, AgentContext{
		TargetDir:  targetDir,
		StagingDir: stagingDir,
		Prompt:     prompt,
		PromptPath: promptPath,
		Command:    command,
	})
	result.AgentOutput = output
	if err != nil {
		return result, fmt.Errorf("run external agent: %w", err)
	}
	if output.ExitCode != 0 {
		return result, fmt.Errorf("external agent exited with status %d", output.ExitCode)
	}

	changes, err := detectChanges(targetDir, stagingDir, matcher)
	if err != nil {
		return result, fmt.Errorf("detect external agent changes: %w", err)
	}
	result.Changes = changes
	if len(changes) == 0 {
		result.ValidationPassed = true
		return result, nil
	}
	if options.MaxChangedFiles > 0 && len(changes) > options.MaxChangedFiles {
		return result, fmt.Errorf("external agent changed %d files, over limit %d", len(changes), options.MaxChangedFiles)
	}

	validationResults := runValidations(ctx, stagingDir, changes, options)
	result.ValidationResults = validationResults
	result.ValidationPassed = validationsPassed(validationResults)
	if !result.ValidationPassed {
		return result, nil
	}
	if !options.Apply {
		return result, nil
	}

	backupDir, manifestPath, err := applyChanges(targetDir, stagingDir, changes, options.Now)
	if err != nil {
		return result, fmt.Errorf("apply external agent changes: %w", err)
	}
	result.BackupDir = backupDir
	result.ManifestPath = manifestPath
	result.Applied = true
	result.RollbackAvailable = true
	result.RollbackCommand = "ai-logfixer-rollback -manifest " + manifestPath
	return result, nil
}

func Rollback(manifestPath string) error {
	raw, err := os.ReadFile(filepath.Clean(manifestPath))
	if err != nil {
		return fmt.Errorf("read rollback manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("decode rollback manifest: %w", err)
	}
	for index := len(manifest.Changes) - 1; index >= 0; index-- {
		change := manifest.Changes[index]
		targetPath := filepath.Join(manifest.TargetDir, filepath.FromSlash(change.Path))
		switch change.Type {
		case ChangeCreate:
			if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove created file %s: %w", change.Path, err)
			}
		case ChangeModify, ChangeDelete:
			if change.BackupPath == "" {
				return fmt.Errorf("rollback manifest missing backup path for %s", change.Path)
			}
			if err := copyFile(change.BackupPath, targetPath, 0o644); err != nil {
				return fmt.Errorf("restore %s: %w", change.Path, err)
			}
		default:
			return fmt.Errorf("unsupported rollback change type %q for %s", change.Type, change.Path)
		}
	}
	return nil
}

func buildPrompt(task string) string {
	return strings.TrimSpace(`You are an external remediation agent working for ai-logfixer.

You are operating in a staging copy of the target application, not the live target. Modify files directly in the staging copy when you have enough evidence.

Rules:
- Keep the patch focused on the reported failure.
- Prefer the existing framework and project conventions.
- Do not edit generated/vendor dependency directories.
- Do not make destructive filesystem changes.
- Add or update focused tests when the project already has an obvious test pattern.
- If the evidence is insufficient for a safe patch, leave the tree unchanged and explain why.

Task and evidence:
` + task + `
`)
}

func buildAgentCommand(options Options, targetDir string, stagingDir string, promptPath string) ([]string, error) {
	commandLine := strings.TrimSpace(options.AgentCommand)
	if commandLine == "" {
		commandLine = `opencode run --file {prompt_file} "Use the attached ai-logfixer prompt file and modify this staging project."`
	}
	command, err := splitCommandLine(commandLine)
	if err != nil {
		return nil, fmt.Errorf("parse agent command: %w", err)
	}
	if len(command) == 0 {
		return nil, errors.New("agent command is empty")
	}
	if options.AgentModel != "" {
		command = append(command, "--model", options.AgentModel)
	}
	if options.AgentName != "" {
		command = append(command, "--agent", options.AgentName)
	}
	for index, arg := range command {
		arg = strings.ReplaceAll(arg, "{prompt_file}", promptPath)
		arg = strings.ReplaceAll(arg, "{staging_dir}", stagingDir)
		arg = strings.ReplaceAll(arg, "{target_dir}", targetDir)
		command[index] = arg
	}
	return command, nil
}

func runAgentCommand(ctx context.Context, agent AgentContext) (CommandOutput, error) {
	cmd := exec.CommandContext(ctx, agent.Command[0], agent.Command[1:]...)
	cmd.Dir = agent.StagingDir
	cmd.Stdin = strings.NewReader(agent.Prompt)
	cmd.Env = append(os.Environ(),
		"AI_LOGFIXER_TARGET_DIR="+agent.TargetDir,
		"AI_LOGFIXER_STAGING_DIR="+agent.StagingDir,
		"AI_LOGFIXER_PROMPT_FILE="+agent.PromptPath,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return CommandOutput{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
	}, err
}

func runValidations(ctx context.Context, stagingDir string, changes []Change, options Options) []ValidationResult {
	var results []ValidationResult
	if options.AutoPHPLint {
		results = append(results, runPHPLintValidations(ctx, stagingDir, changes)...)
	}
	for _, command := range options.ValidationCommands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		results = append(results, runShellValidation(ctx, stagingDir, command))
	}
	return results
}

func runPHPLintValidations(ctx context.Context, stagingDir string, changes []Change) []ValidationResult {
	if _, err := exec.LookPath("php"); err != nil {
		return nil
	}
	var results []ValidationResult
	for _, change := range changes {
		if change.Type == ChangeDelete || !strings.HasSuffix(strings.ToLower(change.Path), ".php") {
			continue
		}
		relPath := filepath.FromSlash(change.Path)
		cmd := exec.CommandContext(ctx, "php", "-l", relPath)
		cmd.Dir = stagingDir
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		results = append(results, ValidationResult{
			Command:  "php -l " + change.Path,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: exitCode(err),
			Passed:   err == nil,
		})
	}
	return results
}

func runShellValidation(ctx context.Context, workDir string, command string) ValidationResult {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Dir = workDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return ValidationResult{
		Command:  command,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
		Passed:   err == nil,
	}
}

func validationsPassed(results []ValidationResult) bool {
	for _, result := range results {
		if !result.Passed {
			return false
		}
	}
	return true
}

func detectChanges(targetDir string, stagingDir string, matcher excludeMatcher) ([]Change, error) {
	before, err := snapshotDir(targetDir, matcher)
	if err != nil {
		return nil, err
	}
	after, err := snapshotDir(stagingDir, matcher)
	if err != nil {
		return nil, err
	}

	var changes []Change
	for path, beforeFile := range before {
		afterFile, ok := after[path]
		if !ok {
			changes = append(changes, Change{Path: path, Type: ChangeDelete})
			continue
		}
		if beforeFile.hash != afterFile.hash {
			changes = append(changes, Change{Path: path, Type: ChangeModify, Size: afterFile.size})
		}
	}
	for path, afterFile := range after {
		if _, ok := before[path]; ok {
			continue
		}
		changes = append(changes, Change{Path: path, Type: ChangeCreate, Size: afterFile.size})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func snapshotDir(root string, matcher excludeMatcher) (map[string]fileSnapshot, error) {
	files := map[string]fileSnapshot{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if matcher.skip(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = fileSnapshot{hash: hash, size: info.Size(), mode: info.Mode()}
		return nil
	}); err != nil {
		return nil, err
	}
	return files, nil
}

func applyChanges(targetDir string, stagingDir string, changes []Change, now time.Time) (string, string, error) {
	backupDir := filepath.Join(targetDir, ".ai-logfixer-backups", "external-"+now.Format("20060102T150405Z"))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", "", err
	}

	manifest := Manifest{
		TargetDir: targetDir,
		BackupDir: backupDir,
		CreatedAt: now,
		Changes:   make([]ManifestChange, 0, len(changes)),
	}
	for _, change := range changes {
		targetPath := filepath.Join(targetDir, filepath.FromSlash(change.Path))
		stagingPath := filepath.Join(stagingDir, filepath.FromSlash(change.Path))
		manifestChange := ManifestChange{Path: change.Path, Type: change.Type}

		switch change.Type {
		case ChangeCreate:
			if err := copyFile(stagingPath, targetPath, 0o644); err != nil {
				return "", "", err
			}
		case ChangeModify:
			backupPath := filepath.Join(backupDir, "original", filepath.FromSlash(change.Path))
			if err := copyFile(targetPath, backupPath, 0o644); err != nil {
				return "", "", err
			}
			if err := copyFile(stagingPath, targetPath, 0o644); err != nil {
				return "", "", err
			}
			manifestChange.BackupPath = backupPath
		case ChangeDelete:
			backupPath := filepath.Join(backupDir, "original", filepath.FromSlash(change.Path))
			if err := copyFile(targetPath, backupPath, 0o644); err != nil {
				return "", "", err
			}
			if err := os.Remove(targetPath); err != nil {
				return "", "", err
			}
			manifestChange.BackupPath = backupPath
		default:
			return "", "", fmt.Errorf("unsupported change type %q", change.Type)
		}
		manifest.Changes = append(manifest.Changes, manifestChange)
	}

	manifestPath := filepath.Join(backupDir, "rollback-manifest.json")
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		return "", "", err
	}
	return backupDir, manifestPath, nil
}

func copyDir(src string, dst string, matcher excludeMatcher) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if matcher.skip(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		targetPath := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, targetPath, info.Mode().Perm())
	})
}

func copyFile(src string, dst string, mode os.FileMode) error {
	input, err := os.Open(filepath.Clean(src))
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(filepath.Clean(dst), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer output.Close()
	_, err = io.Copy(output, input)
	return err
}

func hashFile(path string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:]), nil
}

type excludeMatcher struct {
	names    map[string]struct{}
	prefixes []string
}

func newExcludeMatcher(extra []string) excludeMatcher {
	matcher := excludeMatcher{
		names: map[string]struct{}{
			".git":                 {},
			".ai-logfixer-backups": {},
			"node_modules":         {},
			"vendor":               {},
		},
	}
	for _, value := range extra {
		value = strings.Trim(strings.ReplaceAll(value, `\`, `/`), "/")
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			matcher.prefixes = append(matcher.prefixes, value)
		} else {
			matcher.names[value] = struct{}{}
		}
	}
	return matcher
}

func (m excludeMatcher) skip(rel string) bool {
	rel = strings.Trim(strings.ReplaceAll(filepath.ToSlash(rel), `\`, `/`), "/")
	if rel == "" || rel == "." {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if _, ok := m.names[part]; ok {
			return true
		}
	}
	for _, prefix := range m.prefixes {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
}

func splitCommandLine(input string) ([]string, error) {
	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, char := range input {
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == ' ' || char == '\t' || char == '\n' || char == '\r' {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(char)
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
