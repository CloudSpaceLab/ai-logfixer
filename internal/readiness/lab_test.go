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

func TestManifestDefinesDockerLiveReadinessAssertions(t *testing.T) {
	t.Parallel()

	manifest, err := LoadManifest(filepath.Join("..", "..", "labs", "readiness", "lab.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest should validate: %v", err)
	}

	for _, scenario := range manifest.Scenarios {
		if scenario.ContainerAppDir == "" {
			t.Fatalf("scenario %s must define container_app_dir for live log path mapping", scenario.ID)
		}
		if scenario.LiveProbeURL == "" {
			t.Fatalf("scenario %s must define live_probe_url", scenario.ID)
		}
		if scenario.ExpectedBrokenStatus == 0 {
			t.Fatalf("scenario %s must define expected_broken_status", scenario.ID)
		}
		if scenario.ExpectedFixedStatus != 200 {
			t.Fatalf("scenario %s should recover to HTTP 200, got %d", scenario.ID, scenario.ExpectedFixedStatus)
		}
		if scenario.FixedBodyContains == "" {
			t.Fatalf("scenario %s must define fixed_body_contains", scenario.ID)
		}
		if len(scenario.DockerValidationCommands) == 0 {
			t.Fatalf("scenario %s must define docker_validation_commands", scenario.ID)
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

func TestRunLocalSmokeCanPatchInPlaceFromLiveTraceOverrides(t *testing.T) {
	t.Parallel()

	labDir := t.TempDir()
	appDir := filepath.Join(labDir, "apps", "python-mini")
	traceDir := filepath.Join(labDir, "live-traces")
	mustWriteReadinessFile(t, filepath.Join(appDir, "pyproject.toml"), "[project]\nname = \"mini\"\n")
	mustWriteReadinessFile(t, filepath.Join(appDir, "app.py"), "raise RuntimeError('BROKEN')\n")
	mustWriteReadinessFile(t, filepath.Join(traceDir, "python-mini.log"), "Traceback (most recent call last):\n  File \"{{APP_DIR}}/app.py\", line 1, in app\n    raise RuntimeError('BROKEN')\n")
	manifestPath := filepath.Join(labDir, "lab.json")
	mustWriteReadinessFile(t, manifestPath, `{
  "version": "v1",
  "name": "mini docker lab",
  "docker_compose": "docker-compose.yml",
  "minimum_pass_rate": 1,
  "required_scenarios": ["python-mini"],
  "scenarios": [
    {
      "id": "python-mini",
      "service_name": "python-mini",
      "language": "python",
      "framework": "python",
      "app_dir": "apps/python-mini",
      "stack_trace_file": "traces/python-mini.log",
      "message": "broken",
      "docker_service": "python-mini",
      "validation_commands": ["python3 -m py_compile app.py"],
      "expected_owner_suffix": "app.py",
      "container_app_dir": "/app",
      "live_probe_url": "http://127.0.0.1:18000/orders/readiness",
      "expected_broken_status": 500,
      "expected_fixed_status": 200,
      "fixed_body_contains": "FIXED",
      "docker_validation_commands": ["docker run --rm -v \"$PWD\":/app -w /app python:3.12-slim python -m py_compile app.py"],
      "faults": [{"id": "runtime-error", "description": "runtime error", "mode": "runtime_error"}]
    }
  ]
}`)

	_, err := RunLocalSmoke(context.Background(), SmokeOptions{
		ManifestPath: manifestPath,
		WorkDir:      filepath.Join(labDir, "unused-workdir"),
		TraceDir:     traceDir,
		InPlace:      true,
		Now:          time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
		AgentRunner: func(ctx context.Context, agent agentfix.AgentContext) (agentfix.CommandOutput, error) {
			_ = ctx
			path := filepath.Join(agent.StagingDir, "app.py")
			return agentfix.CommandOutput{ExitCode: 0}, os.WriteFile(path, []byte("print('FIXED')\n"), 0o644)
		},
	})
	if err != nil {
		t.Fatalf("run local smoke: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(appDir, "app.py"))
	if err != nil {
		t.Fatalf("read patched app: %v", err)
	}
	if !strings.Contains(string(raw), "FIXED") {
		t.Fatalf("expected in-place source patch, got %s", raw)
	}
}

func TestNormalizeLiveTraceMapsOnlyContainerSourcePaths(t *testing.T) {
	t.Parallel()

	hostDir := "/tmp/readiness-lab/apps/python-fastapi"
	raw := strings.Join([]string{
		`ai-logfixer-python-fastapi-1  |   File "/usr/local/lib/python3.12/site-packages/fastapi/applications.py", line 1159, in __call__`,
		`ai-logfixer-python-fastapi-1  |   File "/app/main.py", line 11, in get_order`,
		`ai-logfixer-node-express-1  |     at /app/src/server.js:7:11`,
		`ai-logfixer-php-laravel-1  | RuntimeException in /app/app/Http/Controllers/OrderController.php:10`,
	}, "\n")

	mapped := normalizeLiveTrace(raw, "/app", hostDir)

	if strings.Contains(mapped, hostDir+"lications.py") || strings.Contains(mapped, "/site-packages/fastapi"+hostDir) {
		t.Fatalf("framework package path should not be rewritten:\n%s", mapped)
	}
	if strings.Contains(mapped, "  | ") {
		t.Fatalf("docker compose prefixes should be stripped:\n%s", mapped)
	}
	if !strings.Contains(mapped, hostDir+"/main.py") {
		t.Fatalf("expected python app path to be mapped:\n%s", mapped)
	}
	if !strings.Contains(mapped, hostDir+"/src/server.js:7:11") {
		t.Fatalf("expected node app path to be mapped:\n%s", mapped)
	}
	if !strings.Contains(mapped, hostDir+"/app/Http/Controllers/OrderController.php:10") {
		t.Fatalf("expected nested app directory to be mapped once:\n%s", mapped)
	}
	if strings.Contains(mapped, hostDir+hostDir) {
		t.Fatalf("container path should not be mapped twice:\n%s", mapped)
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

func mustWriteReadinessFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
