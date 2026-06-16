package readinesslab

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
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
	ID                      string `json:"id"`
	OperationalLane         string `json:"operational_lane"`
	Runtime                 string `json:"runtime"`
	AppCarrier              string `json:"app_carrier"`
	DockerService           string `json:"docker_service"`
	AppDir                  string `json:"app_dir"`
	PolicyFile              string `json:"policy_file"`
	LiveProbeURL            string `json:"live_probe_url"`
	ExpectedBrokenStatus    int    `json:"expected_broken_status"`
	ExpectedFixedStatus     int    `json:"expected_fixed_status"`
	CandidateExpectedStatus int    `json:"candidate_expected_fixed_status"`
	FixedBodyContains       string `json:"fixed_body_contains"`
	ExpectedCandidateStatus string `json:"expected_candidate_status"`
	UnsafeFixture           bool   `json:"unsafe_fixture"`
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
		if item.UnsafeFixture {
			if item.ExpectedCandidateStatus != "failed" {
				t.Fatalf("%s unsafe fixture must expect a failed candidate status", item.ID)
			}
			if item.ExpectedFixedStatus < 400 {
				t.Fatalf("%s unsafe fixture should remain failed after a blocked repair", item.ID)
			}
		} else if item.ExpectedFixedStatus != 200 {
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

func TestPermissionDriftManifestIncludesDockerSymlinkEscapeFixture(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "labs", "readiness", "lab.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var doc manifest
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	for _, item := range doc.Scenarios {
		if item.ID != "permission-drift-php-laravel-artifacts" {
			continue
		}
		if item.OperationalLane != "permission-drift" {
			t.Fatalf("%s should exercise permission-drift, got %q", item.ID, item.OperationalLane)
		}
		if item.ExpectedCandidateStatus != "failed" {
			t.Fatalf("%s must expect AI LogFixer to block unsafe symlink escape repairs", item.ID)
		}
		if item.CandidateExpectedStatus != 200 {
			t.Fatalf("%s should still ask the candidate to verify normal recovery, got %d", item.ID, item.CandidateExpectedStatus)
		}
		if item.ExpectedFixedStatus != 500 {
			t.Fatalf("%s should remain broken after the blocked repair, got %d", item.ID, item.ExpectedFixedStatus)
		}
		if !item.UnsafeFixture {
			t.Fatalf("%s must be marked unsafe_fixture so drift variants do not pre-heal the escape target", item.ID)
		}
		return
	}
	t.Fatal("permission-drift Docker symlink escape fixture missing from readiness manifest")
}

func TestPermissionDriftManifestCoversIssue45Platforms(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "labs", "readiness", "lab.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var doc manifest
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	required := map[string]bool{
		"go/net-http":           false,
		"node/express":          false,
		"python/flask":          false,
		"php/laravel-style":     false,
		"ruby/lightweight-http": false,
		"java/lightweight-http": false,
	}
	ports := map[string]string{}
	for _, item := range doc.Scenarios {
		if item.OperationalLane != "permission-drift" {
			continue
		}
		key := item.Runtime + "/" + item.AppCarrier
		if _, ok := required[key]; ok {
			required[key] = true
		}
		if item.DockerService == "" {
			t.Fatalf("%s docker_service is required for black-box remediation", item.ID)
		}
		parsed, err := url.Parse(item.LiveProbeURL)
		if err != nil {
			t.Fatalf("%s live_probe_url is invalid: %v", item.ID, err)
		}
		if previous, exists := ports[parsed.Port()]; exists {
			t.Fatalf("%s reuses live probe port %s from %s", item.ID, parsed.Port(), previous)
		}
		ports[parsed.Port()] = item.ID
	}
	for platform, present := range required {
		if !present {
			t.Fatalf("permission-drift platform %q missing from readiness manifest", platform)
		}
	}
}

func TestDockerLabScriptChecksExpectedCandidateStatuses(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"expected_candidate_status",
		"candidate_expectations",
		"json.JSONDecoder().raw_decode",
		"expected_status_for",
		"candidate_expectations_passed",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("script must validate candidate status expectations; missing %q", snippet)
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

func TestDockerLabScriptSkipsUnsafeFixturesWhenApplyingPermissionVariants(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"scenario.get(\"unsafe_fixture\")",
		"continue",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("permission variant injector must not pre-heal unsafe fixtures; missing %q", snippet)
		}
	}
}

func TestDockerLabScriptSupportsPermissionLaneFiltering(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"--lane",
		"AI_LOGFIXER_LANE_FILTER",
		"selected_services",
		"permission-drift",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("script missing lane-filtering snippet %q", snippet)
		}
	}
}

func TestDockerLabScriptSupportsPermissionDriftVariants(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"AI_LOGFIXER_PERMISSION_DRIFT_VARIANT",
		"apply_permission_drift_variant",
		"missing",
		"rm -rf",
		"parent-no-exec",
		"chmod 0666",
		"owner-root",
		"chown -R root:root",
		"group-root",
		"chown -R root:",
		"file-unreadable",
		"file-unwritable",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("script missing permission drift variant snippet %q", snippet)
		}
	}
}

func TestDockerLabScriptGeneratesMissingVariantForEachPermissionScenario(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"if variant == \"missing\":",
		"if target.get(\"kind\") == \"dir\":",
		"commands.append(\"rm -rf \" + shlex.quote(path_for(target)))",
		"print(\"\\t\".join([scenario[\"docker_service\"], \" && \".join(commands)]))",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("missing permission variant must emit rm commands inside each scenario loop; missing %q", snippet)
		}
	}
}

func TestPermissionDriftPoliciesAllowlistNestedParents(t *testing.T) {
	root := repoRoot(t)
	policyPath := filepath.Join(root, "labs", "readiness", "policies", "permission-drift-php-laravel-style-policy.json")
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read php permission policy: %v", err)
	}
	var policy struct {
		AllowedPaths []string `json:"allowed_paths"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("decode php permission policy: %v", err)
	}

	required := map[string]bool{
		"storage":      false,
		"storage/logs": false,
	}
	for _, path := range policy.AllowedPaths {
		if _, ok := required[path]; ok {
			required[path] = true
		}
	}
	for path, present := range required {
		if !present {
			t.Fatalf("php permission policy must allowlist nested parent path %q for parent execute/search drift", path)
		}
	}
}

func TestDockerLabScriptGeneratesParentNoExecForTopLevelPermissionPaths(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"if variant == \"parent-no-exec\":",
		"seen = set()",
		"if target.get(\"kind\") == \"dir\" and str(parent) in (\"\", \".\"):",
		"search_path = \"/app/\" + str(target_path)",
		"commands.append(\"chmod 0666 \" + shlex.quote(search_path))",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("parent-no-exec variant must remove execute/search permission from top-level runtime directories; missing %q", snippet)
		}
	}
}

func TestPermissionDriftPoliciesDeclareFileTargets(t *testing.T) {
	root := repoRoot(t)
	policies, err := filepath.Glob(filepath.Join(root, "labs", "readiness", "policies", "permission-drift-*-policy.json"))
	if err != nil {
		t.Fatalf("glob permission policies: %v", err)
	}
	if len(policies) == 0 {
		t.Fatal("expected permission-drift policies")
	}

	for _, policyPath := range policies {
		raw, err := os.ReadFile(policyPath)
		if err != nil {
			t.Fatalf("read policy %s: %v", policyPath, err)
		}
		var policy struct {
			Framework         string   `json:"framework"`
			AllowedPaths      []string `json:"allowed_paths"`
			PermissionTargets []struct {
				Path         string `json:"path"`
				Kind         string `json:"kind"`
				Access       string `json:"access"`
				ExpectedMode string `json:"expected_mode"`
			} `json:"permission_targets"`
		}
		if err := json.Unmarshal(raw, &policy); err != nil {
			t.Fatalf("decode policy %s: %v", policyPath, err)
		}
		if len(policy.PermissionTargets) == 0 {
			if strings.TrimSpace(policy.Framework) == "" {
				t.Fatalf("%s targetless permission policy must declare framework inference", filepath.Base(policyPath))
			}
			if len(policy.AllowedPaths) > 0 {
				t.Fatalf("%s must not mix allowed_paths with targetless framework inference", filepath.Base(policyPath))
			}
			continue
		}
		var hasReadableFile, hasWritableFile bool
		for _, target := range policy.PermissionTargets {
			if target.Kind != "file" {
				continue
			}
			if target.Path == "" || target.ExpectedMode == "" {
				t.Fatalf("%s file target must declare path and expected_mode: %+v", filepath.Base(policyPath), target)
			}
			switch target.Access {
			case "read":
				hasReadableFile = true
			case "write":
				hasWritableFile = true
			default:
				t.Fatalf("%s file target must declare read or write access: %+v", filepath.Base(policyPath), target)
			}
		}
		if !hasReadableFile || !hasWritableFile {
			t.Fatalf("%s must declare both readable and writable file permission targets", filepath.Base(policyPath))
		}
	}
}

func TestPermissionDriftPoliciesCoverGroupWritableRuntimeTarget(t *testing.T) {
	root := repoRoot(t)
	policies, err := filepath.Glob(filepath.Join(root, "labs", "readiness", "policies", "permission-drift-*-policy.json"))
	if err != nil {
		t.Fatalf("glob permission policies: %v", err)
	}
	if len(policies) == 0 {
		t.Fatal("expected permission-drift policies")
	}

	for _, policyPath := range policies {
		raw, err := os.ReadFile(policyPath)
		if err != nil {
			t.Fatalf("read policy %s: %v", policyPath, err)
		}
		var policy struct {
			PermissionTargets []struct {
				Path          string `json:"path"`
				ExpectedOwner string `json:"expected_owner"`
				ExpectedGroup string `json:"expected_group"`
				ExpectedMode  string `json:"expected_mode"`
			} `json:"permission_targets"`
		}
		if err := json.Unmarshal(raw, &policy); err != nil {
			t.Fatalf("decode policy %s: %v", policyPath, err)
		}
		for _, target := range policy.PermissionTargets {
			if target.ExpectedOwner == "root" && target.ExpectedGroup == "app" && (strings.HasSuffix(target.ExpectedMode, "775") || strings.HasSuffix(target.ExpectedMode, "664")) {
				return
			}
		}
	}
	t.Fatal("permission drift policies must include at least one runtime target whose access depends on group ownership")
}

func TestDockerLabScriptGeneratesFileLevelPermissionVariants(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"permission_targets",
		"file-unreadable|file-unwritable)",
		"file_targets = [",
		"if not file_targets:",
		"target.get(\"kind\") == \"file\"",
		"target.get(\"access\") == \"read\"",
		"commands.append(\"chmod 0000 \" + shlex.quote(path_for(target)))",
		"target.get(\"access\") == \"write\"",
		"commands.append(\"chmod 0444 \" + shlex.quote(path_for(target)))",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("file-level permission variants missing snippet %q", snippet)
		}
	}
}

func TestDockerLabScriptGeneratesGroupRootWithExpectedModeForEachPermissionScenario(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"if variant == \"group-root\":",
		"expected_group = group(policy, target)",
		"chown -R root:root \" + shlex.quote(container_path)",
		"chmod \" + shlex.quote(mode) + \" \" + shlex.quote(container_path)",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("group-root variant must set wrong group while preserving expected mode; missing %q", snippet)
		}
	}
}

func TestDockerLabScriptGeneratesOwnerRootWithExpectedModeForEachPermissionScenario(t *testing.T) {
	root := repoRoot(t)
	scriptPath := filepath.Join(root, "labs", "readiness", "bin", "run-docker-lab.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read docker lab script: %v", err)
	}
	script := string(raw)

	requiredSnippets := []string{
		"if variant == \"owner-root\":",
		"mode = expected_mode(policy, target)",
		"chown -R root:root \" + shlex.quote(container_path)",
		"chmod \" + shlex.quote(mode) + \" \" + shlex.quote(container_path)",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("owner-root variant must set wrong owner/group while preserving expected mode; missing %q", snippet)
		}
	}
}

func TestPermissionEnduranceRunnerExposesLongRunningBlackBoxLoop(t *testing.T) {
	root := repoRoot(t)
	runnerPath := filepath.Join(root, "labs", "readiness", "bin", "run-permission-endurance.py")
	output, err := exec.Command("python3", runnerPath, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("permission endurance runner help failed: %v\n%s", err, string(output))
	}
	help := string(output)

	requiredSnippets := []string{
		"--cycles",
		"--duration-seconds",
		"--candidate-command",
		"--seed",
		"--variants",
		"permission-drift",
		"endurance-report.json",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(help, snippet) {
			t.Fatalf("permission endurance runner help missing %q:\n%s", snippet, help)
		}
	}

	output, err = exec.Command("python3", runnerPath, "--candidate-command", "true").CombinedOutput()
	if err == nil {
		t.Fatalf("permission endurance runner should reject runs without --cycles or --duration-seconds:\n%s", string(output))
	}
	if !strings.Contains(string(output), "requires --cycles or --duration-seconds") {
		t.Fatalf("permission endurance runner missing bounded-run error:\n%s", string(output))
	}
}

func TestPermissionEnduranceRunnerAcceptsParentNoExecVariant(t *testing.T) {
	root := repoRoot(t)
	runnerPath := filepath.Join(root, "labs", "readiness", "bin", "run-permission-endurance.py")
	artifacts := filepath.Join(t.TempDir(), "permission-endurance")
	command := exec.Command(
		"python3",
		runnerPath,
		"--candidate-command", "true",
		"--cycles", "1",
		"--variants", "parent-no-exec",
		"--artifacts", artifacts,
		"--lab-script", "true",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("expected stubbed lab run to fail readiness, got success:\n%s", string(output))
	}
	if strings.Contains(string(output), "unsupported permission-drift variants") {
		t.Fatalf("parent-no-exec must be an accepted permission variant:\n%s", string(output))
	}
	if !strings.Contains(string(output), `"cycles": 1`) {
		t.Fatalf("expected runner to execute one stubbed cycle, got:\n%s", string(output))
	}
}

func TestPermissionEnduranceRunnerAcceptsOwnerRootVariant(t *testing.T) {
	root := repoRoot(t)
	runnerPath := filepath.Join(root, "labs", "readiness", "bin", "run-permission-endurance.py")
	artifacts := filepath.Join(t.TempDir(), "permission-endurance")
	command := exec.Command(
		"python3",
		runnerPath,
		"--candidate-command", "true",
		"--cycles", "1",
		"--variants", "owner-root",
		"--artifacts", artifacts,
		"--lab-script", "true",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("expected stubbed lab run to fail readiness, got success:\n%s", string(output))
	}
	if strings.Contains(string(output), "unsupported permission-drift variants") {
		t.Fatalf("owner-root must be an accepted permission variant:\n%s", string(output))
	}
	if !strings.Contains(string(output), `"cycles": 1`) {
		t.Fatalf("expected runner to execute one stubbed cycle, got:\n%s", string(output))
	}
}

func TestPermissionEnduranceRunnerAcceptsGroupRootVariant(t *testing.T) {
	root := repoRoot(t)
	runnerPath := filepath.Join(root, "labs", "readiness", "bin", "run-permission-endurance.py")
	artifacts := filepath.Join(t.TempDir(), "permission-endurance")
	command := exec.Command(
		"python3",
		runnerPath,
		"--candidate-command", "true",
		"--cycles", "1",
		"--variants", "group-root",
		"--artifacts", artifacts,
		"--lab-script", "true",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("expected stubbed lab run to fail readiness, got success:\n%s", string(output))
	}
	if strings.Contains(string(output), "unsupported permission-drift variants") {
		t.Fatalf("group-root must be an accepted permission variant:\n%s", string(output))
	}
	if !strings.Contains(string(output), `"cycles": 1`) {
		t.Fatalf("expected runner to execute one stubbed cycle, got:\n%s", string(output))
	}
}

func TestPermissionEnduranceRunnerAcceptsFileLevelVariants(t *testing.T) {
	root := repoRoot(t)
	runnerPath := filepath.Join(root, "labs", "readiness", "bin", "run-permission-endurance.py")
	for _, variant := range []string{"file-unreadable", "file-unwritable"} {
		t.Run(variant, func(t *testing.T) {
			artifacts := filepath.Join(t.TempDir(), "permission-endurance")
			command := exec.Command(
				"python3",
				runnerPath,
				"--candidate-command", "true",
				"--cycles", "1",
				"--variants", variant,
				"--artifacts", artifacts,
				"--lab-script", "true",
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("expected stubbed lab run to fail readiness, got success:\n%s", string(output))
			}
			if strings.Contains(string(output), "unsupported permission-drift variants") {
				t.Fatalf("%s must be an accepted permission variant:\n%s", variant, string(output))
			}
			if !strings.Contains(string(output), `"cycles": 1`) {
				t.Fatalf("expected runner to execute one stubbed cycle, got:\n%s", string(output))
			}
		})
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
