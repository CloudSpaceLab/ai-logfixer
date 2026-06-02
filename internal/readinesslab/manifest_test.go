package readinesslab

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type manifest struct {
	Version             string     `json:"version"`
	Name                string     `json:"name"`
	ExpectedCurrentMode string     `json:"expected_current_mode"`
	RequiredLanes       []string   `json:"required_lanes"`
	Scenarios           []scenario `json:"scenarios"`
}

type scenario struct {
	ID                   string `json:"id"`
	OperationalLane      string `json:"operational_lane"`
	Runtime              string `json:"runtime"`
	AppCarrier           string `json:"app_carrier"`
	AppDir               string `json:"app_dir"`
	PolicyFile           string `json:"policy_file"`
	LiveProbeURL         string `json:"live_probe_url"`
	ExpectedBrokenStatus int    `json:"expected_broken_status"`
	ExpectedFixedStatus  int    `json:"expected_fixed_status"`
	FixedBodyContains    string `json:"fixed_body_contains"`
}

func TestOperationalReadinessManifestCoversRequiredLanes(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "labs", "readiness", "lab.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var doc manifest
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if doc.ExpectedCurrentMode != "benchmark-fails-without-candidate" {
		t.Fatalf("expected current mode should document benchmark failure, got %q", doc.ExpectedCurrentMode)
	}

	required := map[string]bool{
		"config-drift":       false,
		"package-regression": false,
		"permission-drift":   false,
		"restart-reload":     false,
	}
	for _, lane := range doc.RequiredLanes {
		if _, ok := required[lane]; ok {
			required[lane] = true
		}
	}
	for lane, present := range required {
		if !present {
			t.Fatalf("required lane %q missing from manifest", lane)
		}
	}

	seen := map[string]bool{}
	for _, item := range doc.Scenarios {
		if item.ID == "" {
			t.Fatal("scenario id is required")
		}
		if item.OperationalLane == "" {
			t.Fatalf("%s operational_lane is required", item.ID)
		}
		if _, ok := required[item.OperationalLane]; !ok {
			t.Fatalf("%s uses unexpected operational lane %q", item.ID, item.OperationalLane)
		}
		seen[item.OperationalLane] = true
		if item.AppDir == "" || item.PolicyFile == "" || item.LiveProbeURL == "" {
			t.Fatalf("%s must define app_dir, policy_file, and live_probe_url", item.ID)
		}
		if item.ExpectedBrokenStatus < 400 {
			t.Fatalf("%s should start with a failing HTTP status", item.ID)
		}
		if item.ExpectedFixedStatus != 200 {
			t.Fatalf("%s should recover to HTTP 200", item.ID)
		}
		if item.FixedBodyContains == "" {
			t.Fatalf("%s fixed_body_contains is required", item.ID)
		}
		if _, err := os.Stat(filepath.Join(root, "labs", "readiness", item.AppDir)); err != nil {
			t.Fatalf("%s app dir missing: %v", item.ID, err)
		}
		if _, err := os.Stat(filepath.Join(root, "labs", "readiness", item.PolicyFile)); err != nil {
			t.Fatalf("%s policy file missing: %v", item.ID, err)
		}
	}
	for lane := range required {
		if !seen[lane] {
			t.Fatalf("no scenario covers lane %q", lane)
		}
	}
}

func TestDockerLabScriptSeparatesFixtureHealthFromBenchmark(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"--mode fixture-health",
		"--mode benchmark",
		"AI_LOGFIXER_CANDIDATE_COMMAND",
		"AI_LOGFIXER_OPERATIONAL_LANE",
		"benchmark-fails-without-candidate",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("script missing %q", snippet)
		}
	}

	forbidden := []string{"safe-runtime-fixer", "marker-agent"}
	for _, snippet := range forbidden {
		if strings.Contains(script, snippet) {
			t.Fatalf("script must not include answer-key fixer %q", snippet)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}
