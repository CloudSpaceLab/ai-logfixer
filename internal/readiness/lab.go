package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/agentfix"
	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/resolver"
)

type Manifest struct {
	Version           string     `json:"version"`
	Name              string     `json:"name"`
	DockerCompose     string     `json:"docker_compose"`
	MinimumPassRate   float64    `json:"minimum_pass_rate"`
	RequiredScenarios []string   `json:"required_scenarios"`
	Scenarios         []Scenario `json:"scenarios"`
}

type Scenario struct {
	ID                       string   `json:"id"`
	ServiceName              string   `json:"service_name"`
	Language                 string   `json:"language"`
	Framework                string   `json:"framework"`
	AppDir                   string   `json:"app_dir"`
	StackTraceFile           string   `json:"stack_trace_file"`
	Message                  string   `json:"message"`
	DockerService            string   `json:"docker_service"`
	ValidationCommands       []string `json:"validation_commands"`
	DockerValidationCommands []string `json:"docker_validation_commands"`
	ExpectedOwnerSuffix      string   `json:"expected_owner_suffix"`
	ContainerAppDir          string   `json:"container_app_dir"`
	LiveProbeURL             string   `json:"live_probe_url"`
	ExpectedBrokenStatus     int      `json:"expected_broken_status"`
	ExpectedFixedStatus      int      `json:"expected_fixed_status"`
	FixedBodyContains        string   `json:"fixed_body_contains"`
	Faults                   []Fault  `json:"faults"`
}

type Fault struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Mode        string `json:"mode"`
}

type SmokeOptions struct {
	ManifestPath        string
	WorkDir             string
	TraceDir            string
	Now                 time.Time
	AgentCommand        string
	AgentModel          string
	AgentName           string
	Concurrency         int
	KeepWorkdir         bool
	InPlace             bool
	UseDockerValidation bool
	MaxFiles            int
	AgentRunner         agentfix.AgentRunner
}

type SmokeReport struct {
	ManifestName string           `json:"manifest_name"`
	StartedAt    time.Time        `json:"started_at"`
	Total        int              `json:"total"`
	Passed       int              `json:"passed"`
	PassRate     float64          `json:"pass_rate"`
	Results      []ScenarioResult `json:"results"`
}

type ScenarioResult struct {
	ScenarioID         string                        `json:"scenario_id"`
	Language           string                        `json:"language"`
	Framework          string                        `json:"framework"`
	OwnerFile          string                        `json:"owner_file"`
	DiagnosisStatus    contractsv1.DiagnosisStatus   `json:"diagnosis_status"`
	RemediationStatus  contractsv1.RemediationStatus `json:"remediation_status"`
	RemediationSummary string                        `json:"remediation_summary,omitempty"`
	Outcome            string                        `json:"outcome"`
	Passed             bool                          `json:"passed"`
	Error              string                        `json:"error,omitempty"`
	AgentExitCode      int                           `json:"agent_exit_code,omitempty"`
	RollbackAvailable  bool                          `json:"rollback_available"`
}

func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	var errs []error
	if strings.TrimSpace(m.Version) == "" {
		errs = append(errs, errors.New("version is required"))
	}
	if strings.TrimSpace(m.Name) == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if strings.TrimSpace(m.DockerCompose) == "" {
		errs = append(errs, errors.New("docker_compose is required"))
	}
	if m.MinimumPassRate <= 0 || m.MinimumPassRate > 1 {
		errs = append(errs, errors.New("minimum_pass_rate must be between 0 and 1"))
	}
	seen := map[string]bool{}
	for index, scenario := range m.Scenarios {
		prefix := fmt.Sprintf("scenarios[%d]", index)
		if strings.TrimSpace(scenario.ID) == "" {
			errs = append(errs, fmt.Errorf("%s.id is required", prefix))
		}
		if seen[scenario.ID] {
			errs = append(errs, fmt.Errorf("%s.id duplicates %q", prefix, scenario.ID))
		}
		seen[scenario.ID] = true
		if scenario.Language == "" || scenario.Framework == "" {
			errs = append(errs, fmt.Errorf("%s language and framework are required", prefix))
		}
		if scenario.AppDir == "" || scenario.StackTraceFile == "" {
			errs = append(errs, fmt.Errorf("%s app_dir and stack_trace_file are required", prefix))
		}
		if scenario.Message == "" {
			errs = append(errs, fmt.Errorf("%s message is required", prefix))
		}
		if scenario.DockerService == "" {
			errs = append(errs, fmt.Errorf("%s docker_service is required", prefix))
		}
		if len(scenario.ValidationCommands) == 0 {
			errs = append(errs, fmt.Errorf("%s validation_commands are required", prefix))
		}
		if scenario.ExpectedFixedStatus != 0 {
			if strings.TrimSpace(scenario.LiveProbeURL) == "" {
				errs = append(errs, fmt.Errorf("%s live_probe_url is required when expected_fixed_status is set", prefix))
			}
			if strings.TrimSpace(scenario.FixedBodyContains) == "" {
				errs = append(errs, fmt.Errorf("%s fixed_body_contains is required when expected_fixed_status is set", prefix))
			}
		}
	}
	for _, required := range m.RequiredScenarios {
		if !seen[required] {
			errs = append(errs, fmt.Errorf("required scenario %q is missing", required))
		}
	}
	return errors.Join(errs...)
}

func (m Manifest) ScenarioByID(id string) (Scenario, bool) {
	for _, scenario := range m.Scenarios {
		if scenario.ID == id {
			return scenario, true
		}
	}
	return Scenario{}, false
}

func RunLocalSmoke(ctx context.Context, options SmokeOptions) (SmokeReport, error) {
	if strings.TrimSpace(options.ManifestPath) == "" {
		return SmokeReport{}, errors.New("manifest path is required")
	}
	if strings.TrimSpace(options.WorkDir) == "" && !options.InPlace {
		return SmokeReport{}, errors.New("work dir is required")
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if options.Concurrency <= 0 {
		options.Concurrency = 1
	}
	manifest, err := LoadManifest(options.ManifestPath)
	if err != nil {
		return SmokeReport{}, err
	}
	if err := manifest.Validate(); err != nil {
		return SmokeReport{}, err
	}
	baseDir := filepath.Dir(filepath.Clean(options.ManifestPath))
	report := SmokeReport{
		ManifestName: manifest.Name,
		StartedAt:    options.Now,
		Total:        len(manifest.Scenarios),
		Results:      make([]ScenarioResult, len(manifest.Scenarios)),
	}

	type job struct {
		index    int
		scenario Scenario
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	workerCount := options.Concurrency
	if workerCount > len(manifest.Scenarios) {
		workerCount = len(manifest.Scenarios)
	}
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for current := range jobs {
				result, err := runScenario(ctx, baseDir, options, current.scenario)
				if err != nil {
					result = ScenarioResult{
						ScenarioID: current.scenario.ID,
						Language:   current.scenario.Language,
						Framework:  current.scenario.Framework,
						Outcome:    "error",
						Error:      err.Error(),
					}
				}
				result.Passed = result.Outcome == "succeeded" &&
					result.DiagnosisStatus == contractsv1.DiagnosisStatusComplete &&
					result.RemediationStatus == contractsv1.RemediationStatusSucceeded &&
					result.RollbackAvailable
				report.Results[current.index] = result
			}
		}()
	}
	for index, scenario := range manifest.Scenarios {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return report, ctx.Err()
		case jobs <- job{index: index, scenario: scenario}:
		}
	}
	close(jobs)
	wg.Wait()

	for _, result := range report.Results {
		if result.Passed {
			report.Passed++
		}
	}
	if report.Total > 0 {
		report.PassRate = float64(report.Passed) / float64(report.Total)
	}
	if report.PassRate < manifest.MinimumPassRate {
		return report, fmt.Errorf("readiness pass rate %.2f below required %.2f", report.PassRate, manifest.MinimumPassRate)
	}
	return report, nil
}

func runScenario(ctx context.Context, baseDir string, options SmokeOptions, scenario Scenario) (ScenarioResult, error) {
	sourceDir := filepath.Join(baseDir, filepath.FromSlash(scenario.AppDir))
	targetDir := filepath.Join(options.WorkDir, scenario.ID)
	if options.InPlace {
		targetDir = sourceDir
	} else if err := copyTree(sourceDir, targetDir); err != nil {
		return ScenarioResult{}, fmt.Errorf("%s copy app: %w", scenario.ID, err)
	}
	tracePath := filepath.Join(baseDir, filepath.FromSlash(scenario.StackTraceFile))
	if strings.TrimSpace(options.TraceDir) != "" {
		tracePath = filepath.Join(options.TraceDir, scenario.ID+".log")
	}
	rawTrace, err := os.ReadFile(tracePath)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("%s read trace: %w", scenario.ID, err)
	}
	trace := strings.ReplaceAll(string(rawTrace), "{{APP_DIR}}", targetDir)
	trace = normalizeLiveTrace(trace, scenario.ContainerAppDir, targetDir)
	validationCommands := scenario.ValidationCommands
	if options.UseDockerValidation && len(scenario.DockerValidationCommands) > 0 {
		validationCommands = scenario.DockerValidationCommands
	}
	runResult, err := resolver.Run(ctx, resolver.Options{
		ServiceName:        scenario.ServiceName,
		TargetDir:          targetDir,
		StackTrace:         trace,
		Message:            scenario.Message,
		Apply:              true,
		ValidationCommands: validationCommands,
		AgentCommand:       options.AgentCommand,
		AgentModel:         options.AgentModel,
		AgentName:          options.AgentName,
		KeepAgentWorkdir:   options.KeepWorkdir,
		MaxChangedFiles:    options.MaxFiles,
		AgentRunner:        options.AgentRunner,
		Now:                options.Now,
	})
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("%s run resolver: %w", scenario.ID, err)
	}
	result := ScenarioResult{
		ScenarioID:         scenario.ID,
		Language:           runResult.Profile.Language,
		Framework:          runResult.Profile.Framework,
		OwnerFile:          runResult.SourceOwner.File,
		DiagnosisStatus:    runResult.Diagnosis.Status,
		RemediationStatus:  runResult.RemediationPlan.Status,
		RemediationSummary: runResult.RemediationPlan.Summary,
		Outcome:            runResult.Receipt.Outcome,
		RollbackAvailable:  runResult.AgentResult != nil && runResult.AgentResult.RollbackAvailable,
	}
	if runResult.AgentResult != nil {
		result.AgentExitCode = runResult.AgentResult.AgentOutput.ExitCode
	}
	return result, nil
}

func normalizeLiveTrace(raw string, containerAppDir string, hostAppDir string) string {
	trace := stripComposeLogPrefixes(raw)
	if strings.TrimSpace(containerAppDir) == "" {
		return trace
	}
	containerDir := strings.TrimRight(filepath.ToSlash(containerAppDir), "/")
	hostDir := filepath.ToSlash(hostAppDir)
	if containerDir == "" || containerDir == "." || hostDir == "" {
		return trace
	}
	return mapContainerAppPaths(trace, containerDir, hostDir)
}

func stripComposeLogPrefixes(raw string) string {
	lines := strings.Split(raw, "\n")
	for index, line := range lines {
		if before, after, ok := strings.Cut(line, " | "); ok && strings.Contains(before, "-") {
			lines[index] = after
		}
	}
	return strings.Join(lines, "\n")
}

func mapContainerAppPaths(raw string, containerDir string, hostDir string) string {
	var builder strings.Builder
	for index := 0; index < len(raw); {
		if strings.HasPrefix(raw[index:], containerDir) &&
			hasContainerPathPrefixBoundary(raw, index) &&
			hasContainerPathSuffixBoundary(raw, index, len(containerDir)) {
			builder.WriteString(hostDir)
			index += len(containerDir)
			continue
		}
		builder.WriteByte(raw[index])
		index++
	}
	return builder.String()
}

func hasContainerPathPrefixBoundary(raw string, start int) bool {
	if start == 0 {
		return true
	}
	switch raw[start-1] {
	case ' ', '\n', '\r', '\t', '"', '\'', '(', '[', '{':
		return true
	default:
		return false
	}
}

func hasContainerPathSuffixBoundary(raw string, start int, length int) bool {
	next := start + length
	if next >= len(raw) {
		return true
	}
	switch raw[next] {
	case '/', ':', '"', '\'', ')', ' ', '\n', '\r', '\t':
		return true
	default:
		return false
	}
}

func copyTree(source string, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(target, 0o755)
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, raw, info.Mode())
	})
}

func (m Manifest) SortedScenarioIDs() []string {
	ids := make([]string, 0, len(m.Scenarios))
	for _, scenario := range m.Scenarios {
		ids = append(ids, scenario.ID)
	}
	sort.Strings(ids)
	return ids
}
