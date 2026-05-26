package remediation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SourceEdit struct {
	Path   string
	Before string
	After  string
}

type SourceFileOptions struct {
	Edit      SourceEdit
	BackupDir string
	Now       time.Time
	Restart   func(context.Context) error
	Verify    func(context.Context) error
}

type SourceFileResult struct {
	BackupPath string `json:"backup_path"`
	Applied    bool   `json:"applied"`
	Restarted  bool   `json:"restarted"`
	Verified   bool   `json:"verified"`
	RolledBack bool   `json:"rolled_back"`
}

func ApplySourceEdit(ctx context.Context, options SourceFileOptions) (SourceFileResult, error) {
	if options.Edit.Path == "" {
		return SourceFileResult{}, errors.New("source edit path is required")
	}
	if options.Edit.Before == "" {
		return SourceFileResult{}, errors.New("source edit before text is required")
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}

	path, err := filepath.Abs(filepath.Clean(options.Edit.Path))
	if err != nil {
		return SourceFileResult{}, fmt.Errorf("resolve source path: %w", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return SourceFileResult{}, fmt.Errorf("read source file: %w", err)
	}
	content := string(raw)
	if !strings.Contains(content, options.Edit.Before) {
		return SourceFileResult{}, errors.New("source edit before text was not found")
	}

	info, err := os.Stat(path)
	if err != nil {
		return SourceFileResult{}, fmt.Errorf("stat source file: %w", err)
	}

	backupDir := options.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(filepath.Dir(path), ".ai-logfixer-backups", "source-"+options.Now.Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return SourceFileResult{}, fmt.Errorf("create source backup directory: %w", err)
	}
	backupPath := filepath.Join(backupDir, filepath.Base(path))
	if err := copyFile(path, backupPath, info.Mode().Perm()); err != nil {
		return SourceFileResult{}, fmt.Errorf("backup source file: %w", err)
	}

	patched := strings.Replace(content, options.Edit.Before, options.Edit.After, 1)
	if err := os.WriteFile(path, []byte(patched), info.Mode().Perm()); err != nil {
		return SourceFileResult{BackupPath: backupPath}, fmt.Errorf("write source patch: %w", err)
	}

	result := SourceFileResult{
		BackupPath: backupPath,
		Applied:    true,
	}

	if options.Restart != nil {
		if err := options.Restart(ctx); err != nil {
			result.RolledBack = rollbackSource(path, backupPath, info.Mode().Perm()) == nil
			return result, fmt.Errorf("restart after source patch: %w", err)
		}
		result.Restarted = true
	}

	if options.Verify != nil {
		if err := options.Verify(ctx); err != nil {
			result.RolledBack = rollbackSource(path, backupPath, info.Mode().Perm()) == nil
			return result, fmt.Errorf("verify source patch: %w", err)
		}
		result.Verified = true
	}

	return result, nil
}

func rollbackSource(path string, backupPath string, mode os.FileMode) error {
	return copyFile(backupPath, path, mode)
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
