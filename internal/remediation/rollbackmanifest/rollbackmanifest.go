package rollbackmanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const Version = "ai-logfixer-rollback-manifest/v1"

type Action string

const (
	ActionRestoreFile       Action = "restore_file"
	ActionRemoveCreatedPath Action = "remove_created_path"
	ActionChmod             Action = "chmod"
)

type Manifest struct {
	Version   string    `json:"version"`
	AppRoot   string    `json:"app_root"`
	CreatedAt time.Time `json:"created_at"`
	Entries   []Entry   `json:"entries"`
}

type Entry struct {
	Action     Action `json:"action"`
	Path       string `json:"path"`
	BackupPath string `json:"backup_path,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

type Result struct {
	ManifestPath string         `json:"manifest_path"`
	Executed     []ExecutedStep `json:"executed"`
}

type ExecutedStep struct {
	Action Action `json:"action"`
	Path   string `json:"path"`
}

func New(appRoot string, now time.Time, entries []Entry) Manifest {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return Manifest{
		Version:   Version,
		AppRoot:   filepath.Clean(appRoot),
		CreatedAt: now.UTC(),
		Entries:   entries,
	}
}

func Write(path string, manifest Manifest) error {
	manifest.Version = defaultString(manifest.Version, Version)
	if manifest.CreatedAt.IsZero() {
		manifest.CreatedAt = time.Now().UTC()
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Clean(path), raw, 0o600)
}

func Execute(manifestPath string) (Result, error) {
	result := Result{ManifestPath: manifestPath}
	raw, err := os.ReadFile(filepath.Clean(manifestPath))
	if err != nil {
		return result, fmt.Errorf("read rollback manifest: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return result, fmt.Errorf("decode rollback manifest: %w", err)
	}
	if manifest.Version != Version {
		return result, fmt.Errorf("unsupported rollback manifest version %q", manifest.Version)
	}
	root, err := filepath.Abs(filepath.Clean(manifest.AppRoot))
	if err != nil {
		return result, err
	}
	if root == "" || root == "." {
		return result, errors.New("rollback manifest app root is required")
	}

	for index := len(manifest.Entries) - 1; index >= 0; index-- {
		entry := manifest.Entries[index]
		target, err := resolveSafe(root, entry.Path)
		if err != nil {
			return result, err
		}
		if err := executeEntry(root, target, entry); err != nil {
			return result, err
		}
		result.Executed = append(result.Executed, ExecutedStep{Action: entry.Action, Path: target})
	}
	return result, nil
}

func executeEntry(root string, target string, entry Entry) error {
	switch entry.Action {
	case ActionRestoreFile:
		backup, err := resolveSafe(root, entry.BackupPath)
		if err != nil {
			return err
		}
		if err := restoreFile(backup, target); err != nil {
			return err
		}
		if entry.Mode != "" {
			mode, err := parseMode(entry.Mode)
			if err != nil {
				return err
			}
			return os.Chmod(target, mode)
		}
	case ActionRemoveCreatedPath:
		return os.RemoveAll(target)
	case ActionChmod:
		mode, err := parseMode(entry.Mode)
		if err != nil {
			return err
		}
		return os.Chmod(target, mode)
	default:
		return fmt.Errorf("unsupported rollback action %q", entry.Action)
	}
	return nil
}

func restoreFile(source string, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(filepath.Clean(source))
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(filepath.Clean(target), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func resolveSafe(root string, rawPath string) (string, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", errors.New("rollback entry path is required")
	}
	if filepath.IsAbs(rawPath) {
		return "", fmt.Errorf("rollback path %q must be relative to app root", rawPath)
	}
	clean := filepath.Clean(rawPath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("rollback path %q escapes app root", rawPath)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("rollback path %q escapes app root", rawPath)
	}
	return target, nil
}

func parseMode(value string) (os.FileMode, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "0")
	if value == "" {
		return 0, errors.New("mode is required")
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, err
	}
	return os.FileMode(parsed), nil
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
