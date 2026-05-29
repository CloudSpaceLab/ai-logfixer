package resolver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/agentfix"
	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestRunResolvesInterpretedLanguageMatrixWithSandboxAndRollback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		language    string
		framework   string
		setup       func(*testing.T, string) (sourcePath string, trace string, validations []string)
		sourceCheck string
	}{
		{
			name:      "php laravel",
			language:  "php",
			framework: "laravel",
			setup: func(t *testing.T, targetDir string) (string, string, []string) {
				writeFile(t, filepath.Join(targetDir, "artisan"), "#!/usr/bin/env php\n")
				writeFile(t, filepath.Join(targetDir, "composer.json"), `{"autoload":{"psr-4":{"App\\":"app/"}}}`)
				sourcePath := filepath.Join(targetDir, "app", "Http", "Controllers", "OrderController.php")
				writeFile(t, sourcePath, `<?php
namespace App\Http\Controllers;

class OrderController {
    public function show() {
        return "BROKEN";
    }
}
`)
				trace := `[2026-05-29 08:13:41] production.ERROR: RuntimeException: database unavailable in ` + sourcePath + `:5
Stack trace:
#0 ` + sourcePath + `(5): App\Http\Controllers\OrderController->show()
#1 {main}`
				return sourcePath, trace, []string{"php -l app/Http/Controllers/OrderController.php"}
			},
			sourceCheck: "return \"FIXED\";",
		},
		{
			name:      "python fastapi",
			language:  "python",
			framework: "fastapi",
			setup: func(t *testing.T, targetDir string) (string, string, []string) {
				writeFile(t, filepath.Join(targetDir, "pyproject.toml"), "[project]\nname = \"orders\"\n")
				sourcePath := filepath.Join(targetDir, "app", "main.py")
				writeFile(t, sourcePath, `from fastapi import FastAPI

app = FastAPI()

@app.get("/orders/{order_id}")
def get_order(order_id: str):
    return "BROKEN"
`)
				trace := `Traceback (most recent call last):
  File "` + sourcePath + `", line 7, in get_order
    return load_order(order_id)
RuntimeError: database unavailable`
				return sourcePath, trace, []string{"python3 -m py_compile app/main.py"}
			},
			sourceCheck: `return "FIXED"`,
		},
		{
			name:      "node nestjs",
			language:  "node",
			framework: "nestjs",
			setup: func(t *testing.T, targetDir string) (string, string, []string) {
				writeFile(t, filepath.Join(targetDir, "package.json"), `{"dependencies":{"@nestjs/core":"latest"}}`)
				sourcePath := filepath.Join(targetDir, "src", "orders.controller.js")
				writeFile(t, sourcePath, `class OrdersController {
  getOrder(id) {
    return "BROKEN";
  }
}
module.exports = { OrdersController };
`)
				trace := `Error: database unavailable
    at OrdersController.getOrder (` + sourcePath + `:3:11)
    at Layer.handleRequest (/srv/app/node_modules/router/lib/layer.js:152:17)`
				return sourcePath, trace, []string{"node --check src/orders.controller.js"}
			},
			sourceCheck: `return "FIXED";`,
		},
		{
			name:      "ruby rails",
			language:  "ruby",
			framework: "rails",
			setup: func(t *testing.T, targetDir string) (string, string, []string) {
				writeFile(t, filepath.Join(targetDir, "Gemfile"), "gem 'rails'\n")
				writeFile(t, filepath.Join(targetDir, "config", "routes.rb"), "Rails.application.routes.draw do\nend\n")
				sourcePath := filepath.Join(targetDir, "app", "controllers", "orders_controller.rb")
				writeFile(t, sourcePath, `class OrdersController < ApplicationController
  def show
    "BROKEN"
  end
end
`)
				trace := `RuntimeError (database unavailable):
  ` + sourcePath + `:3:in 'show'
  app/services/order_loader.rb:42:in 'load_order'`
				return sourcePath, trace, []string{"ruby -c app/controllers/orders_controller.rb"}
			},
			sourceCheck: `"FIXED"`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			targetDir := t.TempDir()
			sourcePath, trace, validations := tc.setup(t, targetDir)

			result, err := Run(context.Background(), Options{
				ServiceName:        strings.ReplaceAll(tc.name, " ", "-"),
				TargetDir:          targetDir,
				StackTrace:         trace,
				Message:            "database unavailable",
				Apply:              true,
				ValidationCommands: validations,
				Now:                time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC),
				AgentRunner:        replaceBrokenWithFixedAgent(t),
			})
			if err != nil {
				t.Fatalf("run resolver: %v", err)
			}

			if result.Profile.Language != tc.language || result.Profile.Framework != tc.framework {
				t.Fatalf("unexpected profile: %+v", result.Profile)
			}
			if result.SourceOwner.File == "" || !samePath(result.SourceOwner.File, sourcePath) {
				t.Fatalf("expected source owner %s, got %+v", sourcePath, result.SourceOwner)
			}
			if result.Diagnosis.Status != contractsv1.DiagnosisStatusComplete {
				t.Fatalf("expected complete diagnosis, got %+v", result.Diagnosis)
			}
			if err := result.Diagnosis.Validate(); err != nil {
				t.Fatalf("diagnosis should validate: %v", err)
			}
			if result.RemediationPlan.Status != contractsv1.RemediationStatusSucceeded {
				t.Fatalf("expected succeeded remediation plan, got %+v", result.RemediationPlan)
			}
			if err := result.RemediationPlan.Validate(); err != nil {
				t.Fatalf("remediation plan should validate: %v", err)
			}
			if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
				t.Fatalf("expected succeeded attempt, got %+v", result.Attempt)
			}
			if err := result.Attempt.Validate(); err != nil {
				t.Fatalf("attempt should validate: %v", err)
			}
			if result.Receipt.Outcome != "succeeded" {
				t.Fatalf("expected succeeded receipt, got %+v", result.Receipt)
			}
			if err := result.Receipt.Validate(); err != nil {
				t.Fatalf("receipt should validate: %v", err)
			}
			if result.AgentResult == nil || !result.AgentResult.Applied || !result.AgentResult.RollbackAvailable {
				t.Fatalf("expected applied agent result with rollback: %+v", result.AgentResult)
			}

			raw, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatalf("read patched source: %v", err)
			}
			if !strings.Contains(string(raw), tc.sourceCheck) {
				t.Fatalf("expected patched source to contain %q:\n%s", tc.sourceCheck, raw)
			}

			if err := agentfix.Rollback(result.AgentResult.ManifestPath); err != nil {
				t.Fatalf("rollback applied patch: %v", err)
			}
			rolledBack, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatalf("read rolled back source: %v", err)
			}
			if !strings.Contains(string(rolledBack), "BROKEN") {
				t.Fatalf("expected rollback to restore BROKEN marker:\n%s", rolledBack)
			}
		})
	}
}

func TestRunEscalatesSafelyWhenNoAgentIsConfigured(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	writeFile(t, filepath.Join(targetDir, "package.json"), `{"dependencies":{"express":"latest"}}`)
	sourcePath := filepath.Join(targetDir, "src", "orders.js")
	writeFile(t, sourcePath, `exports.show = () => "BROKEN";`)
	trace := `Error: database unavailable
    at exports.show (` + sourcePath + `:1:21)`

	result, err := Run(context.Background(), Options{
		ServiceName: "node-no-agent",
		TargetDir:   targetDir,
		StackTrace:  trace,
		Message:     "database unavailable",
		Apply:       true,
		Now:         time.Date(2026, 5, 29, 9, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run resolver without agent: %v", err)
	}

	if result.Profile.Language != "node" || result.Profile.Framework != "express" {
		t.Fatalf("unexpected profile: %+v", result.Profile)
	}
	if result.Diagnosis.Status != contractsv1.DiagnosisStatusComplete {
		t.Fatalf("expected diagnosis from available context, got %+v", result.Diagnosis)
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected safe escalation, got %+v", result.RemediationPlan)
	}
	if result.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked risk without agent, got %+v", result.RemediationPlan)
	}
	if result.Receipt.Outcome != "escalated" {
		t.Fatalf("expected escalated receipt, got %+v", result.Receipt)
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
				case ".git", "node_modules", "vendor", ".venv":
					return filepath.SkipDir
				}
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

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func samePath(left string, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return leftAbs == rightAbs
}
