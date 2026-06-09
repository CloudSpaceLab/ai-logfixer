package truth

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
)

type Environment string

const (
	EnvironmentUnknown    Environment = "unknown"
	EnvironmentProduction Environment = "production"
	EnvironmentStaging    Environment = "staging"
	EnvironmentLocal      Environment = "local"
)

type ErrorSignal struct {
	Service     string      `json:"service"`
	Framework   string      `json:"framework"`
	Source      string      `json:"source"`
	Message     string      `json:"message"`
	Environment Environment `json:"environment"`
	StackTrace  string      `json:"stack_trace"`
	ObservedAt  time.Time   `json:"observed_at"`
}

type StackTrace struct {
	Raw    string       `json:"raw"`
	Frames []StackFrame `json:"frames"`
}

type StackFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Package  string `json:"package"`
}

type SuppressionSite struct {
	File       string  `json:"file"`
	Function   string  `json:"function"`
	Kind       string  `json:"kind"`
	Evidence   string  `json:"evidence"`
	CanReveal  bool    `json:"can_reveal"`
	Confidence float64 `json:"confidence"`
}

type RevealPlan struct {
	ID             string      `json:"id"`
	Environment    Environment `json:"environment"`
	Safe           bool        `json:"safe"`
	Strategy       string      `json:"strategy"`
	Steps          []string    `json:"steps"`
	BlockedReasons []string    `json:"blocked_reasons"`
}

type SourceOwner struct {
	File       string  `json:"file"`
	Function   string  `json:"function"`
	Language   string  `json:"language"`
	Framework  string  `json:"framework"`
	Confidence float64 `json:"confidence"`
}

type FixBundle struct {
	ID              string   `json:"id"`
	Summary         string   `json:"summary"`
	Prompt          string   `json:"prompt"`
	Files           []string `json:"files"`
	Evidence        []string `json:"evidence"`
	Redacted        bool     `json:"redacted"`
	ValidationHints []string `json:"validation_hints"`
}

type TruthRecoveryResult struct {
	Signal           ErrorSignal       `json:"signal"`
	StackTrace       StackTrace        `json:"stack_trace"`
	SuppressionSites []SuppressionSite `json:"suppression_sites"`
	RevealPlan       RevealPlan        `json:"reveal_plan"`
	SourceOwner      SourceOwner       `json:"source_owner"`
	FixBundle        FixBundle         `json:"fix_bundle"`
}

type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func PublicResult(result TruthRecoveryResult) TruthRecoveryResult {
	result.Signal.Message = Redact(result.Signal.Message)
	result.Signal.StackTrace = Redact(result.Signal.StackTrace)
	result.StackTrace.Raw = Redact(result.StackTrace.Raw)
	for index := range result.SuppressionSites {
		result.SuppressionSites[index].Evidence = Redact(result.SuppressionSites[index].Evidence)
	}
	return result
}

type FrameworkTruthAdapter interface {
	Name() string
	DetectSuppression(ErrorSignal, []SourceFile) []SuppressionSite
	PlanReveal(ErrorSignal, []SuppressionSite) RevealPlan
	BuildFixBundle(TruthRecoveryResult) (FixBundle, error)
}

type StackTraceResolver interface {
	Resolve(ErrorSignal) (StackTrace, SourceOwner, error)
}

type SuppressionDetector interface {
	Detect(ErrorSignal, []SourceFile) []SuppressionSite
}

type RevealPlanner interface {
	Plan(ErrorSignal, []SuppressionSite) RevealPlan
}

type FixBundleBuilder interface {
	Build(TruthRecoveryResult) (FixBundle, error)
}

type RecoveryOptions struct {
	Signal        ErrorSignal
	SourceFiles   []SourceFile
	Resolver      StackTraceResolver
	Detector      SuppressionDetector
	RevealPlanner RevealPlanner
	BundleBuilder FixBundleBuilder
}

func Recover(options RecoveryOptions) (TruthRecoveryResult, error) {
	signal := options.Signal
	if signal.Environment == "" {
		signal.Environment = EnvironmentUnknown
	}
	detector := options.Detector
	if detector == nil {
		detector = StaticSuppressionDetector{}
	}
	planner := options.RevealPlanner
	if planner == nil {
		planner = DefaultRevealPlanner{}
	}
	builder := options.BundleBuilder
	if builder == nil {
		builder = DefaultFixBundleBuilder{}
	}

	sites := detector.Detect(signal, options.SourceFiles)
	result := TruthRecoveryResult{
		Signal:           signal,
		SuppressionSites: sites,
	}

	if strings.TrimSpace(signal.StackTrace) != "" {
		resolver := options.Resolver
		if resolver == nil {
			resolver = GoStackTraceResolver{}
		}
		trace, owner, err := resolver.Resolve(signal)
		if err != nil {
			return result, err
		}
		result.StackTrace = trace
		result.SourceOwner = owner
	}

	result.RevealPlan = planner.Plan(signal, sites)
	if result.SourceOwner.File == "" && strings.TrimSpace(signal.StackTrace) == "" {
		return result, nil
	}
	bundle, err := builder.Build(result)
	if err != nil {
		if strings.TrimSpace(signal.StackTrace) == "" {
			return result, nil
		}
		return result, err
	}
	result.FixBundle = bundle
	return result, nil
}

type GoStackTraceResolver struct {
	TargetDir string
}

func (r GoStackTraceResolver) Resolve(signal ErrorSignal) (StackTrace, SourceOwner, error) {
	raw := strings.TrimSpace(signal.StackTrace)
	if raw == "" {
		return StackTrace{}, SourceOwner{}, errors.New("stack trace is empty")
	}
	trace := StackTrace{Raw: raw, Frames: parseGoFrames(raw)}
	if len(trace.Frames) == 0 {
		return trace, SourceOwner{}, errors.New("stack trace did not contain resolvable frames")
	}
	owner := SourceOwnerFromStack(trace, signal.Framework)
	return trace, owner, nil
}

func SourceOwnerFromStack(trace StackTrace, framework string) SourceOwner {
	for _, frame := range trace.Frames {
		if isApplicationFrame(frame.File) {
			return SourceOwner{
				File:       frame.File,
				Function:   frame.Function,
				Language:   "go",
				Framework:  framework,
				Confidence: 0.9,
			}
		}
	}
	first := trace.Frames[0]
	return SourceOwner{
		File:       first.File,
		Function:   first.Function,
		Language:   "go",
		Framework:  framework,
		Confidence: 0.65,
	}
}

type StaticSuppressionDetector struct{}

func (StaticSuppressionDetector) Detect(signal ErrorSignal, files []SourceFile) []SuppressionSite {
	var sites []SuppressionSite
	for _, file := range files {
		content := file.Content
		if strings.TrimSpace(content) == "" {
			continue
		}
		lower := strings.ToLower(content)
		messageMatch := signal.Message != "" && strings.Contains(content, signal.Message)
		patterns := []struct {
			kind      string
			needle    string
			canReveal bool
		}{
			{kind: "panic_recovery", needle: "recover()", canReveal: true},
			{kind: "custom_error_response", needle: "http.error", canReveal: true},
			{kind: "custom_error_response", needle: "return response", canReveal: true},
			{kind: "exception_catch", needle: "catch", canReveal: true},
			{kind: "framework_error_handler", needle: "render(", canReveal: true},
		}
		for _, pattern := range patterns {
			if strings.Contains(lower, pattern.needle) || messageMatch {
				sites = append(sites, SuppressionSite{
					File:       file.Path,
					Function:   enclosingFunction(content, pattern.needle),
					Kind:       pattern.kind,
					Evidence:   excerptAround(content, signal.Message, pattern.needle),
					CanReveal:  pattern.canReveal,
					Confidence: 0.78,
				})
				break
			}
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Confidence != sites[j].Confidence {
			return sites[i].Confidence > sites[j].Confidence
		}
		return sites[i].File < sites[j].File
	})
	return sites
}

type DefaultRevealPlanner struct {
	IDFactory engine.ContractIDFactory
}

func (p DefaultRevealPlanner) Plan(signal ErrorSignal, sites []SuppressionSite) RevealPlan {
	factory := p.IDFactory
	if factory == (engine.ContractIDFactory{}) {
		factory = engine.NewContractIDFactory()
	}
	environment := signal.Environment
	if environment == "" {
		environment = EnvironmentUnknown
	}
	id := factory.ID("reveal_plan", signal.Service, signal.Framework, string(environment), signal.Message)
	if environment == EnvironmentProduction {
		return RevealPlan{
			ID:          id,
			Environment: environment,
			Safe:        false,
			Strategy:    "blocked_production_reveal",
			Steps: []string{
				"Reproduce the failing request, job, or command in staging or a local copy.",
				"Apply a diagnostic reveal patch only outside production.",
				"Capture and redact the real exception before fix-bundle handoff.",
			},
			BlockedReasons: []string{"production error suppression must not be disabled automatically"},
		}
	}
	if strings.TrimSpace(signal.StackTrace) != "" {
		return RevealPlan{
			ID:          id,
			Environment: environment,
			Safe:        true,
			Strategy:    "stack_trace_available",
			Steps:       []string{"Resolve stack frames to source ownership.", "Build a scoped fix bundle from the trace and owning source files."},
		}
	}
	if len(sites) == 0 {
		return RevealPlan{
			ID:             id,
			Environment:    environment,
			Safe:           false,
			Strategy:       "needs_more_evidence",
			Steps:          []string{"Collect framework logs, request correlation ID, and failing route/job details."},
			BlockedReasons: []string{"no stack trace or error suppression site was found"},
		}
	}
	return RevealPlan{
		ID:          id,
		Environment: environment,
		Safe:        true,
		Strategy:    "staged_diagnostic_reveal",
		Steps: []string{
			"Patch the staging copy to rethrow or log the original exception at the detected suppression site.",
			"Run the failing request, job, or command against the staging copy.",
			"Capture the full stack trace and redact secrets.",
			"Revert the diagnostic reveal patch before any production apply step.",
		},
	}
}

type DefaultFixBundleBuilder struct {
	IDFactory engine.ContractIDFactory
}

func (b DefaultFixBundleBuilder) Build(result TruthRecoveryResult) (FixBundle, error) {
	if result.SourceOwner.File == "" && len(result.StackTrace.Frames) > 0 {
		result.SourceOwner = SourceOwnerFromStack(result.StackTrace, result.Signal.Framework)
	}
	if result.SourceOwner.File == "" {
		return FixBundle{}, errors.New("source owner is required to build a fix bundle")
	}
	factory := b.IDFactory
	if factory == (engine.ContractIDFactory{}) {
		factory = engine.NewContractIDFactory()
	}
	evidence := []string{}
	if result.Signal.Message != "" {
		evidence = append(evidence, "message: "+Redact(result.Signal.Message))
	}
	if result.StackTrace.Raw != "" {
		evidence = append(evidence, "stack_trace:\n"+Redact(result.StackTrace.Raw))
	}
	for _, site := range result.SuppressionSites {
		evidence = append(evidence, "suppression_site: "+site.File+" "+site.Kind+" "+Redact(site.Evidence))
	}
	files := uniqueStrings([]string{result.SourceOwner.File})
	prompt := buildPrompt(result, files, evidence)
	return FixBundle{
		ID:              factory.ID("fix_bundle", result.Signal.Service, result.SourceOwner.File, result.SourceOwner.Function, result.Signal.Message),
		Summary:         "Fix " + result.SourceOwner.File + " using recovered error evidence.",
		Prompt:          prompt,
		Files:           files,
		Evidence:        evidence,
		Redacted:        true,
		ValidationHints: []string{"Run existing tests or the failing request/job reproduction before applying."},
	}, nil
}

func BuildBlockedContracts(signal ErrorSignal, reason string, now time.Time) (contractsv1.RemediationPlan, contractsv1.RemediationAttempt, contractsv1.Receipt, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	incident := engine.IncidentSignal{
		Service:   signal.Service,
		Source:    signal.Source,
		Kind:      "truth_recovery",
		Code:      "truth_reveal_blocked",
		Signature: signal.Message,
		Count:     1,
		Start:     now.Add(-time.Nanosecond),
		End:       now,
		Tags:      []string{"runtime-v2", "truth-recovery"},
	}
	factory := engine.NewContractIDFactory()
	diagnosisID := factory.ID("diag_truth_recovery_blocked", incident.StableParts()...)
	builder := engine.BlockedPlanBuilder{IDFactory: factory, Now: now, Actor: "ai-logfixer-v2"}
	plan := builder.RemediationPlan(diagnosisID, incident, reason)
	attempt := builder.EscalatedAttempt(plan.ID, incident, reason)
	receipt := builder.EscalatedReceipt(diagnosisID, plan.ID, attempt.ID, incident, reason)
	return plan, attempt, receipt, errors.Join(plan.Validate(), attempt.Validate(), receipt.Validate())
}

func Redact(value string) string {
	redactions := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(password|passwd|pwd)\s*=\s*[^\s&]+`),
		regexp.MustCompile(`(?i)(token|api[_-]?key|secret)\s*=\s*[^\s&]+`),
		regexp.MustCompile(`(?i)(bearer)\s+[A-Za-z0-9._~+/=-]+`),
	}
	out := value
	for _, re := range redactions {
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			if index := strings.Index(match, "="); index >= 0 {
				return match[:index+1] + "<redacted>"
			}
			fields := strings.Fields(match)
			if len(fields) > 0 {
				return fields[0] + " <redacted>"
			}
			return "<redacted>"
		})
	}
	return out
}

func parseGoFrames(raw string) []StackFrame {
	lines := strings.Split(raw, "\n")
	var frames []StackFrame
	for index := 0; index < len(lines)-1; index++ {
		function := strings.TrimSpace(lines[index])
		fileLine := strings.TrimSpace(lines[index+1])
		if function == "" || strings.Contains(function, "goroutine ") {
			continue
		}
		frame, ok := parseGoFileLine(fileLine)
		if !ok {
			continue
		}
		frame.Function = function
		frame.Package = packageFromFunction(function)
		frames = append(frames, frame)
	}
	return frames
}

func parseGoFileLine(line string) (StackFrame, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "\t"))
	if line == "" {
		return StackFrame{}, false
	}
	if fields := strings.Fields(line); len(fields) > 0 {
		line = fields[0]
	}
	if index := strings.LastIndex(line, ":"); index >= 0 {
		file := line[:index]
		rest := line[index+1:]
		linePart := rest
		column := 0
		if columnIndex := strings.Index(rest, ":"); columnIndex >= 0 {
			linePart = rest[:columnIndex]
			column, _ = strconv.Atoi(rest[columnIndex+1:])
		}
		lineNumber, err := strconv.Atoi(linePart)
		if err != nil {
			return StackFrame{}, false
		}
		return StackFrame{File: filepath.ToSlash(file), Line: lineNumber, Column: column}, true
	}
	return StackFrame{}, false
}

func packageFromFunction(function string) string {
	if index := strings.LastIndex(function, "."); index >= 0 {
		return function[:index]
	}
	return function
}

func isApplicationFrame(file string) bool {
	file = filepath.ToSlash(file)
	if strings.Contains(file, "/vendor/") || strings.Contains(file, "/pkg/mod/") || strings.Contains(file, "/runtime/") {
		return false
	}
	return strings.Contains(file, "/app/") || strings.Contains(file, "app/") || strings.Contains(file, "/cmd/") || strings.Contains(file, "/internal/")
}

func enclosingFunction(content string, needle string) string {
	index := strings.Index(strings.ToLower(content), strings.ToLower(needle))
	if index < 0 {
		index = 0
	}
	before := content[:index]
	re := regexp.MustCompile(`func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	matches := re.FindAllStringSubmatch(before, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

func excerptAround(content string, message string, needle string) string {
	target := message
	if target == "" || !strings.Contains(content, target) {
		target = needle
	}
	index := strings.Index(strings.ToLower(content), strings.ToLower(target))
	if index < 0 {
		index = 0
	}
	start := index - 160
	if start < 0 {
		start = 0
	}
	end := index + 240
	if end > len(content) {
		end = len(content)
	}
	return strings.TrimSpace(content[start:end])
}

func buildPrompt(result TruthRecoveryResult, files []string, evidence []string) string {
	return strings.TrimSpace(fmt.Sprintf(`You are fixing an AI LogFixer Runtime V2 truth-recovered error.

Service: %s
Framework: %s
Source owner: %s %s

Rules:
- Use only the scoped files unless evidence proves another owner.
- Do not disable production error suppression.
- Keep the patch focused on the recovered root error.
- Add or run the closest available validation.

Files:
%s

Evidence:
%s
`, result.Signal.Service, result.Signal.Framework, result.SourceOwner.File, result.SourceOwner.Function, strings.Join(files, "\n"), strings.Join(evidence, "\n\n")))
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
