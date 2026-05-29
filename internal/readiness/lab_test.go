package readiness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/agentfix"
	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestLoadManifestCoversRequiredProductionReadinessStacks(t *testing.T) {
	t.Parallel()

	manifest, err := LoadManifest(filepath.Join("..", "..", "labs", "readiness", "lab.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest should validate: %v", err)
	}

	required := []string{"php-laravel", "python-fastapi", "python-django", "node-express", "ruby-rails"}
	for _, id := range required {
		scenario, ok := manifest.ScenarioByID(id)
		if !ok {
			t.Fatalf("expected required scenario %q", id)
		}
		if scenario.AppDir == "" || scenario.StackTraceFile == "" {
			t.Fatalf("scenario %s missing app or trace path: %+v", id, scenario)
		}
		if len(scenario.ValidationCommands) == 0 {
			t.Fatalf("scenario %s must define validation commands", id)
		}
		if scenario.DockerService == "" {
			t.Fatalf("scenario %s must map to a docker compose service", id)
		}
	}
}

func TestRunLocalSmokeMatrixUsesResolverSandboxValidationAndRollback(t *testing.T) {
	t.Parallel()

	manifestPath := filepath.Join("..", "..", "labs", "readiness", "lab.json")
	report, err := RunLocalSmoke(context.Background(), SmokeOptions{
		ManifestPath: manifestPath,
		WorkDir:      t.TempDir(),
		Now:          time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
		AgentRunner:  replaceBrokenWithFixedAgent(t),
	})
	if err != nil {
		t.Fatalf("run local smoke: %v", err)
	}

	if len(report.Results) != 5 {
		t.Fatalf("expected 5 scenario results, got %+v", report.Results)
	}
	for _, result := range report.Results {
		if !result.Passed {
			t.Fatalf("scenario %s should be marked passed, got %+v", result.ScenarioID, result)
		}
		if result.Outcome != "succeeded" {
			t.Fatalf("scenario %s should succeed, got %+v", result.ScenarioID, result)
		}
		if result.DiagnosisStatus != contractsv1.DiagnosisStatusComplete {
			t.Fatalf("scenario %s diagnosis should be complete, got %+v", result.ScenarioID, result)
		}
		if result.RemediationStatus != contractsv1.RemediationStatusSucceeded {
			t.Fatalf("scenario %s remediation should succeed, got %+v", result.ScenarioID, result)
		}
		if !result.RollbackAvailable {
			t.Fatalf("scenario %s must record rollback availability", result.ScenarioID)
		}
		if result.OwnerFile == "" {
			t.Fatalf("scenario %s must identify source owner", result.ScenarioID)
		}
	}
}

func TestRunLocalSmokeMatrixRunsScenariosConcurrently(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	active := 0
	maxActive := 0
	baseAgent := replaceBrokenWithFixedAgent(t)
	agent := func(ctx context.Context, agent agentfix.AgentContext) (agentfix.CommandOutput, error) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()

		time.Sleep(25 * time.Millisecond)
		output, err := baseAgent(ctx, agent)

		mu.Lock()
		active--
		mu.Unlock()
		return output, err
	}

	_, err := RunLocalSmoke(context.Background(), SmokeOptions{
		ManifestPath: filepath.Join("..", "..", "labs", "readiness", "lab.json"),
		WorkDir:      t.TempDir(),
		Now:          time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
		Concurrency:  5,
		AgentRunner:  agent,
	})
	if err != nil {
		t.Fatalf("run local smoke: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if maxActive < 2 {
		t.Fatalf("expected concurrent scenario execution, max active agents was %d", maxActive)
	}
}

func replaceBrokenWithFixedAgent(t *testing.T) agentfix.AgentRunner {
	t.Helper()

	return func(ctx context.Context, agent agentfix.AgentContext) (agentfix.CommandOutput, error) {
		_ = ctx
		err := filepath.WalkDir(agent.StagingDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules", "vendor", ".venv", "__pycache__":
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() == "trace.txt" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			next := strings.ReplaceAll(string(raw), "BROKEN", "FIXED")
			if next == string(raw) {
				return nil
			}
			return os.WriteFile(path, []byte(next), 0o644)
		})
		if err != nil {
			return agentfix.CommandOutput{}, err
		}
		return agentfix.CommandOutput{Stdout: "patched BROKEN marker", ExitCode: 0}, nil
	}
}
