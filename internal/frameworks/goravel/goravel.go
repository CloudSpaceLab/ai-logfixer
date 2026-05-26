package goravel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
	"github.com/CloudSpaceLab/ai-logfixer/internal/remediation"
)

type Options struct {
	ServiceName   string
	TargetDir     string
	AccessLogPath string
	Threshold     int
	Window        time.Duration
	Now           time.Time
}

type Analysis struct {
	Failure              FailureGroup
	Route                RouteMapping
	HandlerExcerpt       string
	HandlerSource        string
	PatchSafety          PatchSafety
	InvestigationRequest contractsv1.InvestigationRequest
	Diagnosis            contractsv1.DiagnosisResult
	RemediationPlan      contractsv1.RemediationPlan
}

type ExecutionOptions struct {
	BackupDir string
	Now       time.Time
	Restart   func(context.Context) error
	Verify    func(context.Context) error
}

type ExecutionResult struct {
	SourceFile remediation.SourceFileResult
	Attempt    contractsv1.RemediationAttempt
	Receipt    contractsv1.Receipt
}

type AccessLogEntry struct {
	Timestamp time.Time
	Status    int
	Duration  string
	Remote    string
	Method    string
	Route     string
	Raw       string
}

type FailureThreshold struct {
	ServiceName string
	MinCount    int
	Window      time.Duration
}

type FailureGroup struct {
	ServiceName string
	Method      string
	Route       string
	StatusClass int
	Count       int
	Start       time.Time
	End         time.Time
	Entries     []AccessLogEntry
}

type RouteMapping struct {
	Method         string
	Path           string
	HandlerExpr    string
	ControllerVar  string
	ControllerType string
	HandlerMethod  string
	SourceFile     string
	HandlerFile    string
}

type PatchSafety struct {
	Safe           bool
	Reasons        []string
	PanicLineCount int
	PanicLine      string
}

func Analyze(options Options) (Analysis, error) {
	if options.TargetDir == "" {
		return Analysis{}, errors.New("target directory is required")
	}
	if options.AccessLogPath == "" {
		return Analysis{}, errors.New("access log path is required")
	}
	if options.ServiceName == "" {
		options.ServiceName = filepath.Base(filepath.Clean(options.TargetDir))
	}
	if options.Threshold == 0 {
		options.Threshold = 3
	}
	if options.Window == 0 {
		options.Window = 5 * time.Minute
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}

	targetDir, err := filepath.Abs(filepath.Clean(options.TargetDir))
	if err != nil {
		return Analysis{}, fmt.Errorf("resolve target directory: %w", err)
	}
	options.TargetDir = targetDir

	rawLog, err := os.ReadFile(filepath.Clean(options.AccessLogPath))
	if err != nil {
		return Analysis{}, fmt.Errorf("read access log: %w", err)
	}
	entries := ParseAccessLog(string(rawLog))
	groups := RepeatedFailures(entries, FailureThreshold{
		ServiceName: options.ServiceName,
		MinCount:    options.Threshold,
		Window:      options.Window,
	})
	if len(groups) == 0 {
		return Analysis{}, fmt.Errorf("failure threshold not reached for %s", options.ServiceName)
	}
	failure := groups[0]

	routes, err := DiscoverRoutes(targetDir)
	if err != nil {
		return Analysis{}, fmt.Errorf("discover Goravel routes: %w", err)
	}
	route, ok := FindRoute(routes, failure.Method, failure.Route)
	if !ok {
		return Analysis{}, fmt.Errorf("route %s %s was not found in Goravel routes", failure.Method, failure.Route)
	}

	handlerSource, err := HandlerSource(route.HandlerFile, route.ControllerType, route.HandlerMethod)
	if err != nil {
		return Analysis{}, fmt.Errorf("collect handler source evidence: %w", err)
	}
	handlerExcerpt := safeExcerpt(handlerSource)
	patchSafety := AnalyzePanicPatchSafety(handlerSource)

	investigationRequest := buildInvestigationRequest(options, failure)
	diagnosis := buildDiagnosis(options, failure, route, handlerExcerpt, patchSafety, string(rawLog))
	remediationPlan := buildRemediationPlan(options, failure, route, handlerExcerpt, patchSafety)

	if err := investigationRequest.Validate(); err != nil {
		return Analysis{}, fmt.Errorf("validate investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return Analysis{}, fmt.Errorf("validate diagnosis: %w", err)
	}
	if err := remediationPlan.Validate(); err != nil {
		return Analysis{}, fmt.Errorf("validate remediation plan: %w", err)
	}

	return Analysis{
		Failure:              failure,
		Route:                route,
		HandlerExcerpt:       handlerExcerpt,
		HandlerSource:        handlerSource,
		PatchSafety:          patchSafety,
		InvestigationRequest: investigationRequest,
		Diagnosis:            diagnosis,
		RemediationPlan:      remediationPlan,
	}, nil
}

func ExecutePanicPatch(ctx context.Context, analysis Analysis, options ExecutionOptions) (ExecutionResult, error) {
	if analysis.Route.HandlerFile == "" {
		return ExecutionResult{}, errors.New("analysis route handler file is required")
	}
	if !analysis.PatchSafety.Safe || analysis.RemediationPlan.RiskLevel == contractsv1.SafetyBlocked {
		return ExecutionResult{}, errors.New("Goravel source patch is blocked because no safe allowlisted handler edit is available")
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}

	edit, err := handlerScopedPanicSourceEdit(analysis.Route.HandlerFile, analysis.Route.ControllerType, analysis.Route.HandlerMethod)
	if err != nil {
		return ExecutionResult{}, err
	}

	sourceResult, err := remediation.ApplySourceEdit(ctx, remediation.SourceFileOptions{
		Edit:      edit,
		BackupDir: options.BackupDir,
		Now:       options.Now,
		Restart:   options.Restart,
		Verify:    options.Verify,
	})

	attempt := buildExecutionAttempt(options, analysis, sourceResult, err)
	receipt := buildExecutionReceipt(options, analysis, sourceResult, err)
	if validateErr := attempt.Validate(); validateErr != nil {
		if err != nil {
			return ExecutionResult{SourceFile: sourceResult, Attempt: attempt, Receipt: receipt}, fmt.Errorf("%w; validate failed attempt: %v", err, validateErr)
		}
		return ExecutionResult{}, fmt.Errorf("validate Goravel execution attempt: %w", validateErr)
	}
	if validateErr := receipt.Validate(); validateErr != nil {
		if err != nil {
			return ExecutionResult{SourceFile: sourceResult, Attempt: attempt, Receipt: receipt}, fmt.Errorf("%w; validate failed receipt: %v", err, validateErr)
		}
		return ExecutionResult{}, fmt.Errorf("validate Goravel execution receipt: %w", validateErr)
	}

	result := ExecutionResult{
		SourceFile: sourceResult,
		Attempt:    attempt,
		Receipt:    receipt,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func ParseAccessLog(content string) []AccessLogEntry {
	re := regexp.MustCompile(`^\[HTTP\]\s+([0-9]{4}-[0-9]{2}-[0-9]{2})\s+([0-9:.]+)\s+\|\s+([0-9]{3})\s+\|\s+([^|]+)\|\s+([^|]+)\|\s+([A-Z]+)\s+"([^"]+)"`)
	var entries []AccessLogEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(stripANSI(line))
		if line == "" {
			continue
		}
		match := re.FindStringSubmatch(line)
		if len(match) == 0 {
			continue
		}
		status, err := strconv.Atoi(match[3])
		if err != nil {
			continue
		}
		timestamp, err := parseGoravelTimestamp(match[1] + " " + match[2])
		if err != nil {
			continue
		}
		entries = append(entries, AccessLogEntry{
			Timestamp: timestamp,
			Status:    status,
			Duration:  strings.TrimSpace(match[4]),
			Remote:    strings.TrimSpace(match[5]),
			Method:    strings.ToUpper(strings.TrimSpace(match[6])),
			Route:     strings.TrimSpace(match[7]),
			Raw:       line,
		})
	}
	return entries
}

func RepeatedFailures(entries []AccessLogEntry, threshold FailureThreshold) []FailureGroup {
	if threshold.MinCount == 0 {
		threshold.MinCount = 3
	}
	if threshold.Window == 0 {
		threshold.Window = 5 * time.Minute
	}

	byKey := map[string][]AccessLogEntry{}
	for _, entry := range entries {
		if entry.Status < 400 {
			continue
		}
		key := failureKey(entry)
		byKey[key] = append(byKey[key], entry)
	}

	var groups []FailureGroup
	for _, values := range byKey {
		sort.Slice(values, func(i, j int) bool {
			return values[i].Timestamp.Before(values[j].Timestamp)
		})
		for startIndex := range values {
			var window []AccessLogEntry
			start := values[startIndex].Timestamp
			for _, entry := range values[startIndex:] {
				if entry.Timestamp.Sub(start) > threshold.Window {
					break
				}
				window = append(window, entry)
			}
			if len(window) < threshold.MinCount {
				continue
			}
			first := window[0]
			last := window[len(window)-1]
			groups = append(groups, FailureGroup{
				ServiceName: threshold.ServiceName,
				Method:      first.Method,
				Route:       first.Route,
				StatusClass: (first.Status / 100) * 100,
				Count:       len(window),
				Start:       first.Timestamp,
				End:         last.Timestamp,
				Entries:     append([]AccessLogEntry(nil), window...),
			})
			break
		}
	}

	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].Start.Before(groups[j].Start)
	})
	return groups
}

func DiscoverRoutes(targetDir string) ([]RouteMapping, error) {
	routePath := filepath.Join(targetDir, "routes", "web.go")
	raw, err := os.ReadFile(filepath.Clean(routePath))
	if err != nil {
		return nil, err
	}
	content := string(raw)

	constructors := map[string]string{}
	assignRe := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*controllers\.New([A-Za-z_][A-Za-z0-9_]*)\s*\(\s*\)`)
	for _, match := range assignRe.FindAllStringSubmatch(content, -1) {
		constructors[match[1]] = match[2]
	}

	routeRe := regexp.MustCompile(`facades\.Route\(\)\.(Get|Post|Put|Patch|Delete|Any)\s*\(\s*"([^"]+)"\s*,\s*([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\s*\)`)
	var routes []RouteMapping
	for _, match := range routeRe.FindAllStringSubmatch(content, -1) {
		controllerVar := match[3]
		controllerType := strings.TrimPrefix(constructors[controllerVar], "New")
		if controllerType == "" {
			controllerType = controllerVar
		}
		method := strings.ToUpper(match[1])
		if method == "ANY" {
			method = "*"
		}
		handlerFile := filepath.Join(targetDir, "app", "http", "controllers", snakeCase(controllerType)+".go")
		routes = append(routes, RouteMapping{
			Method:         method,
			Path:           match[2],
			HandlerExpr:    controllerVar + "." + match[4],
			ControllerVar:  controllerVar,
			ControllerType: controllerType,
			HandlerMethod:  match[4],
			SourceFile:     routePath,
			HandlerFile:    handlerFile,
		})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

func FindRoute(routes []RouteMapping, method string, route string) (RouteMapping, bool) {
	method = strings.ToUpper(method)
	for _, item := range routes {
		if item.Path == route && (item.Method == method || item.Method == "*") {
			return item, true
		}
	}
	return RouteMapping{}, false
}

func HandlerExcerpt(path string, controllerType string, method string) (string, error) {
	source, err := HandlerSource(path, controllerType, method)
	if err != nil {
		return "", err
	}
	return safeExcerpt(source), nil
}

func HandlerSource(path string, controllerType string, method string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	content := string(raw)
	excerpt, ok := extractGoMethod(content, controllerType, method)
	if !ok {
		return content, nil
	}
	return excerpt, nil
}

func AnalyzePanicPatchSafety(handlerSource string) PatchSafety {
	var lines []string
	for _, line := range strings.Split(handlerSource, "\n") {
		if strings.Contains(line, "panic(") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	switch len(lines) {
	case 0:
		return PatchSafety{
			Safe:           false,
			Reasons:        []string{"mapped handler does not contain an allowlisted panic line"},
			PanicLineCount: 0,
		}
	case 1:
		return PatchSafety{
			Safe:           true,
			PanicLineCount: 1,
			PanicLine:      lines[0],
			Reasons:        []string{},
		}
	default:
		return PatchSafety{
			Safe:           false,
			Reasons:        []string{"mapped handler contains multiple panic lines and requires manual review"},
			PanicLineCount: len(lines),
			PanicLine:      strings.Join(lines, "\n"),
		}
	}
}

func parseGoravelTimestamp(value string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05.999",
		"2006-01-02 15:04:05",
	}
	var lastErr error
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, value, time.UTC)
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func failureKey(entry AccessLogEntry) string {
	return fmt.Sprintf("%s|%s|%d", entry.Method, entry.Route, (entry.Status/100)*100)
}

func extractGoMethod(content string, controllerType string, method string) (string, bool) {
	pattern := fmt.Sprintf(`func\s*\([^)]*\*?%s\)\s+%s\s*\(`, regexp.QuoteMeta(controllerType), regexp.QuoteMeta(method))
	re := regexp.MustCompile(pattern)
	location := re.FindStringIndex(content)
	if location == nil {
		return "", false
	}
	open := strings.Index(content[location[1]:], "{")
	if open < 0 {
		return safeExcerpt(content[location[0]:]), true
	}
	open += location[1]
	depth := 0
	for index := open; index < len(content); index++ {
		switch content[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[location[0] : index+1], true
			}
		}
	}
	return content[location[0]:], true
}

func buildInvestigationRequest(options Options, failure FailureGroup) contractsv1.InvestigationRequest {
	factory := engine.NewContractIDFactory()
	signal := goravelSignal(options, failure, RouteMapping{})
	return contractsv1.InvestigationRequest{
		ID:              factory.ID("inv_req_goravel_route_failure", signal.StableParts()...),
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "goravel-framework-adapter",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         fmt.Sprintf("Repeated HTTP %d responses for %s %s", failure.StatusClass, failure.Method, failure.Route),
		ErrorCode:       strconv.Itoa(failure.StatusClass),
		TimeWindow: contractsv1.TimeWindow{
			Start: failure.Start,
			End:   failure.End,
		},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "goravel_route_error_spike",
			ErrorCode:     strconv.Itoa(failure.StatusClass),
			Source:        options.AccessLogPath,
			DeployVersion: "unknown",
			Tags:          []string{"goravel", "framework", "http", failure.Method, failure.Route},
		},
		DisplayStatus: "Goravel route failure detected",
		UserMessage:   fmt.Sprintf("I detected repeated failures for %s %s and started a framework-aware investigation.", failure.Method, failure.Route),
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildDiagnosis(options Options, failure FailureGroup, route RouteMapping, handlerExcerpt string, patchSafety PatchSafety, logContent string) contractsv1.DiagnosisResult {
	factory := engine.NewContractIDFactory()
	signal := goravelSignal(options, failure, route)
	parts := signal.StableParts()
	rootCause := fmt.Sprintf("The failing route %s %s maps through %s to %s.%s in %s.", failure.Method, failure.Route, filepath.ToSlash(route.SourceFile), route.ControllerType, route.HandlerMethod, filepath.ToSlash(route.HandlerFile))
	confidence := 0.78
	summary := "Repeated Goravel route failures were mapped to the owning controller handler."
	safety := contractsv1.SafetyBlocked
	recommendations := []contractsv1.RunbookRecommendation{blockedGoravelRecommendation(route, patchSafety, factory.ID("rec_goravel_manual_review", parts...))}
	patchPlan := blockedGoravelPatchPlan(route, handlerExcerpt, patchSafety, factory)
	rollbackPlan := blockedGoravelRollbackPlan(route, patchSafety, factory)
	nextActions := []contractsv1.NextAction{
		{
			ID:          "next_manual_goravel_review",
			Label:       "Review handler",
			ActionType:  "manual_review",
			Description: "Review the mapped handler and provide an explicit source patch.",
			Enabled:     true,
		},
	}
	displayStatus := "Goravel handler mapped; automatic fix blocked"
	userMessage := fmt.Sprintf("I traced %s %s to %s.%s, but no safe allowlisted source patch is available.", failure.Method, failure.Route, route.ControllerType, route.HandlerMethod)
	if patchSafety.Safe {
		rootCause = rootCause + " The handler source contains a panic call in the failing path."
		confidence = 0.88
		summary = "Repeated Goravel route failures are likely caused by a panic in the mapped controller handler."
		safety = contractsv1.SafetyMediumRisk
		recommendations = []contractsv1.RunbookRecommendation{goravelSourcePatchRecommendation(route, factory.ID("rec_goravel_source_patch", parts...))}
		patchPlan = goravelSourcePatchPlan(route, handlerExcerpt, patchSafety, factory)
		rollbackPlan = goravelRollbackPlan(route, factory)
		nextActions = []contractsv1.NextAction{
			{
				ID:          "next_review_goravel_source_patch",
				Label:       "Review source patch",
				ActionType:  "review_patch",
				Description: "Review the controller handler patch and rollback plan before execution.",
				Enabled:     true,
			},
		}
		displayStatus = "Goravel handler mapped"
		userMessage = fmt.Sprintf("I traced %s %s to %s.%s and prepared a source patch preview for review.", failure.Method, failure.Route, route.ControllerType, route.HandlerMethod)
	} else if len(patchSafety.Reasons) > 0 {
		rootCause = rootCause + " Automatic source remediation is blocked: " + strings.Join(patchSafety.Reasons, "; ") + "."
	}

	return contractsv1.DiagnosisResult{
		ID:                   factory.ID("diag_goravel_route_handler", parts...),
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               contractsv1.DiagnosisStatusComplete,
		Summary:              summary,
		Confidence:           confidence,
		SuspectedRootCause:   rootCause,
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        goravelEvidence(options, failure, route, handlerExcerpt, logContent),
		Recommendations:      recommendations,
		PatchPlan:            patchPlan,
		RollbackPlan:         rollbackPlan,
		SafetyClassification: safety,
		DisplayStatus:        displayStatus,
		UserMessage:          userMessage,
		NextActions:          nextActions,
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        factory.ID("tl_goravel_route_diag", parts...),
				Type:      "diagnosis.completed",
				Message:   "Goravel route-to-handler diagnosis completed.",
				Severity:  severityForSafety(safety),
				Timestamp: options.Now,
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildRemediationPlan(options Options, failure FailureGroup, route RouteMapping, handlerExcerpt string, patchSafety PatchSafety) contractsv1.RemediationPlan {
	factory := engine.NewContractIDFactory()
	signal := goravelSignal(options, failure, route)
	parts := signal.StableParts()
	if !patchSafety.Safe {
		reason := "Automatic Goravel source remediation is blocked because " + strings.Join(patchSafety.Reasons, "; ") + "."
		builder := engine.BlockedPlanBuilder{IDFactory: factory, Now: options.Now, Actor: "goravel-framework-adapter"}
		plan := builder.RemediationPlan(factory.ID("diag_goravel_route_handler", parts...), signal, reason)
		plan.ID = factory.ID("rem_plan_goravel_blocked", parts...)
		plan.DiagnosisResultID = factory.ID("diag_goravel_route_handler", parts...)
		plan.Summary = fmt.Sprintf("Automatic source patch blocked for %s.%s.", route.ControllerType, route.HandlerMethod)
		plan.FixPreview = contractsv1.DiffPreview{Before: blockedSourceBefore(route, patchSafety), After: "No automatic change; manual source review required."}
		plan.NextActions = []contractsv1.NextAction{{ID: "next_manual_goravel_patch", Label: "Review source", ActionType: "manual_review", Description: "Inspect the mapped handler and author an explicit patch.", Enabled: true}}
		return plan
	}
	plan := goravelSourcePatchPlan(route, handlerExcerpt, patchSafety, factory)
	return contractsv1.RemediationPlan{
		ID:                factory.ID("rem_plan_goravel_source_patch", parts...),
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: factory.ID("diag_goravel_route_handler", parts...),
		Summary:           fmt.Sprintf("Patch %s.%s and restart the Goravel service before verification.", route.ControllerType, route.HandlerMethod),
		FixPreview:        plan.DiffPreview,
		RollbackPlan:      *goravelRollbackPlan(route, factory),
		RiskLevel:         contractsv1.SafetyMediumRisk,
		ApprovalRequired:  true,
		Status:            contractsv1.RemediationStatusAwaitingApproval,
		DisplayStatus:     "Goravel source patch awaiting approval",
		UserMessage:       "This source-code remediation requires review, approval, restart, and post-fix verification.",
		NextActions: []contractsv1.NextAction{
			{
				ID:          "next_approve_goravel_patch",
				Label:       "Approve patch",
				ActionType:  "approve_remediation",
				Description: "Approve the source patch after reviewing the route evidence and rollback plan.",
				Enabled:     true,
			},
			{
				ID:          "next_restart_after_patch",
				Label:       "Restart service",
				ActionType:  "restart_service",
				Description: "Restart or reload the Goravel service after applying the patch, then re-probe the failing route.",
				Enabled:     false,
			},
		},
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        factory.ID("tl_goravel_plan", parts...),
				Type:      "remediation.plan_created",
				Message:   "Goravel source remediation plan created.",
				Severity:  "info",
				Timestamp: options.Now,
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func goravelEvidence(options Options, failure FailureGroup, route RouteMapping, handlerExcerpt string, logContent string) []contractsv1.EvidenceItem {
	factory := engine.NewContractIDFactory()
	parts := goravelSignal(options, failure, route).StableParts()
	accessLogID := factory.ID("ev_goravel_access_log", parts...)
	routeMapID := factory.ID("ev_goravel_route_map", parts...)
	handlerSourceID := factory.ID("ev_goravel_handler_source", parts...)
	return []contractsv1.EvidenceItem{
		{
			ID:             accessLogID,
			Type:           contractsv1.EvidenceTypeLog,
			Source:         options.AccessLogPath,
			Timestamp:      options.Now,
			Title:          "Repeated Goravel access-log failures",
			Summary:        fmt.Sprintf("The access log contains %d failures for %s %s in the selected window.", failure.Count, failure.Method, failure.Route),
			RawExcerpt:     safeExcerpt(logContent),
			RedactionState: contractsv1.RedactionStateRedacted,
			RelatedIDs:     []string{routeMapID},
			UIHints:        contractsv1.UIHints{Icon: "file-warning", Tone: "danger", Sections: []string{"logs", "evidence"}},
			ExternalRefs:   []contractsv1.ExternalRef{},
			KnowledgeRefs:  []contractsv1.KnowledgeRef{},
		},
		{
			ID:             routeMapID,
			Type:           contractsv1.EvidenceTypeConfig,
			Source:         route.SourceFile,
			Timestamp:      options.Now,
			Title:          "Goravel route-to-handler mapping",
			Summary:        fmt.Sprintf("%s %s maps to %s.%s.", route.Method, route.Path, route.ControllerType, route.HandlerMethod),
			RawExcerpt:     route.HandlerExpr,
			RedactionState: contractsv1.RedactionStateNotNeeded,
			RelatedIDs:     []string{handlerSourceID},
			UIHints:        contractsv1.UIHints{Icon: "route", Tone: "warning", Sections: []string{"routes", "evidence"}},
			ExternalRefs:   []contractsv1.ExternalRef{},
			KnowledgeRefs:  []contractsv1.KnowledgeRef{},
		},
		{
			ID:             handlerSourceID,
			Type:           contractsv1.EvidenceTypeLog,
			Source:         route.HandlerFile,
			Timestamp:      options.Now,
			Title:          "Mapped controller handler source",
			Summary:        fmt.Sprintf("The failing route handler is %s.%s.", route.ControllerType, route.HandlerMethod),
			RawExcerpt:     handlerExcerpt,
			RedactionState: contractsv1.RedactionStateRedacted,
			RelatedIDs:     []string{routeMapID},
			UIHints:        contractsv1.UIHints{Icon: "code", Tone: "warning", Sections: []string{"source", "evidence"}},
			ExternalRefs:   []contractsv1.ExternalRef{},
			KnowledgeRefs:  []contractsv1.KnowledgeRef{},
		},
	}
}

func goravelSourcePatchRecommendation(route RouteMapping, id string) contractsv1.RunbookRecommendation {
	return contractsv1.RunbookRecommendation{
		ID:                  id,
		Title:               "Review and patch the failing Goravel handler",
		Reason:              fmt.Sprintf("The access-log failure maps to %s.%s in %s.", route.ControllerType, route.HandlerMethod, filepath.ToSlash(route.HandlerFile)),
		Confidence:          0.82,
		Steps:               []string{"Review the source patch preview.", "Save a file snapshot before editing.", "Apply the focused handler patch.", "Restart or reload the Goravel app.", "Verify the failing route returns a healthy response."},
		RequiredPermissions: []string{"filesystem:read", "filesystem:write", "service:restart", "http:verify"},
		EstimatedRisk:       contractsv1.SafetyMediumRisk,
		RequiresApproval:    true,
	}
}

func blockedGoravelRecommendation(route RouteMapping, patchSafety PatchSafety, id string) contractsv1.RunbookRecommendation {
	reason := "No safe allowlisted source patch is available."
	if len(patchSafety.Reasons) > 0 {
		reason = strings.Join(patchSafety.Reasons, "; ") + "."
	}
	return contractsv1.RunbookRecommendation{
		ID:                  id,
		Title:               "Escalate Goravel handler for manual patch",
		Reason:              fmt.Sprintf("%s Route maps to %s.%s in %s.", reason, route.ControllerType, route.HandlerMethod, filepath.ToSlash(route.HandlerFile)),
		Confidence:          0.74,
		Steps:               []string{"Review the mapped handler source.", "Author a focused source patch.", "Snapshot the file before applying changes.", "Restart or reload the Goravel app.", "Verify the failing route returns a healthy response."},
		RequiredPermissions: []string{"filesystem:read", "manual_patch:required", "service:restart", "http:verify"},
		EstimatedRisk:       contractsv1.SafetyBlocked,
		RequiresApproval:    true,
	}
}

func goravelSourcePatchPlan(route RouteMapping, handlerExcerpt string, patchSafety PatchSafety, factory engine.ContractIDFactory) *contractsv1.PatchPlan {
	before := patchSafety.PanicLine
	if before == "" {
		before = firstPanicLine(handlerExcerpt)
	}
	if before == "" {
		before = "handler source requires review"
	}
	return &contractsv1.PatchPlan{
		ID:         factory.ID("patch_goravel_handler_source", route.HandlerFile, route.ControllerType, route.HandlerMethod, before),
		TargetType: contractsv1.PatchTargetFile,
		TargetRefs: []string{route.HandlerFile},
		DiffPreview: contractsv1.DiffPreview{
			Before: before,
			After:  "remove the failing handler statement and preserve the existing successful response path",
		},
		RiskLevel:        contractsv1.SafetyMediumRisk,
		RequiresApproval: true,
		BlockedReasons:   []string{},
	}
}

func blockedGoravelPatchPlan(route RouteMapping, handlerExcerpt string, patchSafety PatchSafety, factory engine.ContractIDFactory) *contractsv1.PatchPlan {
	return &contractsv1.PatchPlan{
		ID:         factory.ID("patch_goravel_blocked", route.HandlerFile, route.ControllerType, route.HandlerMethod, strings.Join(patchSafety.Reasons, "|")),
		TargetType: contractsv1.PatchTargetFile,
		TargetRefs: []string{route.HandlerFile},
		DiffPreview: contractsv1.DiffPreview{
			Before: blockedSourceBefore(route, patchSafety),
			After:  "No automatic change; manual source review required.",
		},
		RiskLevel:        contractsv1.SafetyBlocked,
		RequiresApproval: true,
		BlockedReasons:   append([]string(nil), patchSafety.Reasons...),
	}
}

func goravelRollbackPlan(route RouteMapping, factory engine.ContractIDFactory) *contractsv1.RollbackPlan {
	return &contractsv1.RollbackPlan{
		ID:                   factory.ID("rollback_goravel_handler_source", route.HandlerFile, route.ControllerType, route.HandlerMethod),
		RollbackType:         contractsv1.RollbackSnapshot,
		SnapshotRefs:         []string{route.HandlerFile},
		RestoreSteps:         []string{"Restore the controller file snapshot.", "Restart or reload the Goravel service.", "Re-run the failing route verification."},
		Limitations:          []string{"Rollback restores the previous source state and may reintroduce the route failure."},
		RiskLevel:            contractsv1.SafetyMediumRisk,
		RequiresManualReview: false,
	}
}

func blockedGoravelRollbackPlan(route RouteMapping, patchSafety PatchSafety, factory engine.ContractIDFactory) *contractsv1.RollbackPlan {
	return &contractsv1.RollbackPlan{
		ID:                   factory.ID("rollback_goravel_blocked", route.HandlerFile, route.ControllerType, route.HandlerMethod, strings.Join(patchSafety.Reasons, "|")),
		RollbackType:         contractsv1.RollbackUnavailable,
		SnapshotRefs:         []string{},
		RestoreSteps:         []string{},
		Limitations:          []string{"No automatic source patch was applied, so AI LogFixer has no generated source change to roll back."},
		RiskLevel:            contractsv1.SafetyBlocked,
		RequiresManualReview: true,
	}
}

func handlerScopedPanicSourceEdit(path string, controllerType string, method string) (remediation.SourceEdit, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return remediation.SourceEdit{}, fmt.Errorf("read handler source for panic patch: %w", err)
	}
	content := string(raw)
	handlerSource, ok := extractGoMethod(content, controllerType, method)
	if !ok {
		return remediation.SourceEdit{}, errors.New("mapped handler source was not found")
	}
	patchSafety := AnalyzePanicPatchSafety(handlerSource)
	if !patchSafety.Safe {
		return remediation.SourceEdit{}, errors.New("mapped handler does not have exactly one safe panic line to patch")
	}
	lines := strings.SplitAfter(handlerSource, "\n")
	patchedLines := make([]string, 0, len(lines))
	removed := false
	skipNextBlank := false
	for _, line := range lines {
		if skipNextBlank && strings.TrimSpace(line) == "" {
			skipNextBlank = false
			continue
		}
		skipNextBlank = false
		if !strings.Contains(line, "panic(") {
			patchedLines = append(patchedLines, line)
			continue
		}
		removed = true
		skipNextBlank = true
	}
	if !removed {
		return remediation.SourceEdit{}, errors.New("mapped handler does not contain a panic line to patch")
	}
	return remediation.SourceEdit{
		Path:   path,
		Before: handlerSource,
		After:  strings.Join(patchedLines, ""),
	}, nil
}

func buildExecutionAttempt(options ExecutionOptions, analysis Analysis, sourceResult remediation.SourceFileResult, runErr error) contractsv1.RemediationAttempt {
	factory := engine.NewContractIDFactory()
	parts := []string{analysis.RemediationPlan.ID, analysis.Route.HandlerFile, analysis.Route.ControllerType, analysis.Route.HandlerMethod}
	started := options.Now
	finished := options.Now.Add(time.Second)
	status := contractsv1.RemediationStatusSucceeded
	monitorStatus := "healthy"
	display := "Goravel source patch applied and verified"
	message := fmt.Sprintf("Patched %s.%s, restarted the service, and verified the route recovered.", analysis.Route.ControllerType, analysis.Route.HandlerMethod)
	signals := []string{
		"source_patch_applied=true",
		fmt.Sprintf("restarted=%t", sourceResult.Restarted),
		fmt.Sprintf("verified=%t", sourceResult.Verified),
	}
	if sourceResult.BackupPath != "" {
		signals = append(signals, "backup_path="+sourceResult.BackupPath)
	}
	if runErr != nil {
		status = contractsv1.RemediationStatusFailed
		monitorStatus = "failed"
		display = "Goravel source patch failed verification"
		message = "The source patch did not pass restart or verification; rollback was attempted."
		if sourceResult.RolledBack {
			status = contractsv1.RemediationStatusRolledBack
			monitorStatus = "rolled_back"
			display = "Goravel source patch rolled back"
			message = "The source patch failed restart or verification and the original file snapshot was restored."
		}
		signals = append(signals, "error="+runErr.Error(), fmt.Sprintf("rolled_back=%t", sourceResult.RolledBack))
	}

	return contractsv1.RemediationAttempt{
		ID:                  factory.ID("rem_attempt_goravel_source_patch", parts...),
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   analysis.RemediationPlan.ID,
		ApprovalRequestID:   "approved_goravel_source_patch",
		Status:              status,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary: contractsv1.MonitorSummary{
			Status:   monitorStatus,
			Message:  message,
			Signals:  signals,
			Duration: "1s",
		},
		DisplayStatus: display,
		UserMessage:   message,
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        factory.ID("tl_goravel_source_attempt", parts...),
				Type:      "remediation." + string(status),
				Message:   display,
				Severity:  severityForStatus(status),
				Timestamp: finished,
			},
		},
		ExternalRefs: []contractsv1.ExternalRef{},
	}
}

func buildExecutionReceipt(options ExecutionOptions, analysis Analysis, sourceResult remediation.SourceFileResult, runErr error) contractsv1.Receipt {
	factory := engine.NewContractIDFactory()
	attemptID := factory.ID("rem_attempt_goravel_source_patch", analysis.RemediationPlan.ID, analysis.Route.HandlerFile, analysis.Route.ControllerType, analysis.Route.HandlerMethod)
	parts := []string{analysis.Diagnosis.ID, analysis.RemediationPlan.ID, attemptID, analysis.Route.HandlerFile}
	outcome := "succeeded"
	action := "removed failing panic line from Goravel handler"
	after := fmt.Sprintf("%s.%s verified after restart", analysis.Route.ControllerType, analysis.Route.HandlerMethod)
	if runErr != nil {
		outcome = "failed"
		action = "attempted Goravel handler panic patch"
		after = "patch failed before verification completed"
		if sourceResult.RolledBack {
			outcome = "rolled_back"
			after = "original handler snapshot restored"
		}
	}
	return contractsv1.Receipt{
		ID:                   factory.ID("receipt_goravel_source_patch", parts...),
		DiagnosisID:          analysis.Diagnosis.ID,
		RemediationPlanID:    analysis.RemediationPlan.ID,
		RemediationAttemptID: attemptID,
		ActionTaken:          action,
		Actor:                "goravel-framework-adapter",
		Approver:             "approved_goravel_source_patch",
		Timestamp:            options.Now.Add(2 * time.Second),
		BeforeState:          fmt.Sprintf("handler=%s.%s backup=%s", analysis.Route.ControllerType, analysis.Route.HandlerMethod, sourceResult.BackupPath),
		AfterState:           after,
		Outcome:              outcome,
		Summary:              "AI LogFixer executed the Goravel source patch workflow and recorded restart/verification outcome.",
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        factory.ID("tl_goravel_source_receipt", parts...),
				Type:      "receipt.created",
				Message:   "Receipt recorded for Goravel source remediation.",
				Severity:  severityForOutcome(outcome),
				Timestamp: options.Now.Add(2 * time.Second),
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
	}
}

func goravelSignal(options Options, failure FailureGroup, route RouteMapping) engine.IncidentSignal {
	start := failure.Start
	end := failure.End
	if start.IsZero() {
		start = options.Now.Add(-time.Nanosecond)
	}
	if end.IsZero() || !end.After(start) {
		end = start.Add(time.Nanosecond)
	}
	source := options.AccessLogPath
	signature := failure.Method + " " + failure.Route
	if route.HandlerFile != "" {
		source = route.HandlerFile
		signature = route.ControllerType + "." + route.HandlerMethod
	}
	return engine.IncidentSignal{
		Service:     options.ServiceName,
		Source:      source,
		Kind:        "goravel_route_failure",
		Method:      failure.Method,
		Route:       failure.Route,
		StatusClass: failure.StatusClass,
		Code:        strconv.Itoa(failure.StatusClass),
		Signature:   signature,
		Count:       failure.Count,
		Start:       start,
		End:         end,
		Tags:        []string{"goravel", "framework", "http"},
	}
}

func blockedSourceBefore(route RouteMapping, patchSafety PatchSafety) string {
	detail := "handler=" + route.ControllerType + "." + route.HandlerMethod
	if patchSafety.PanicLineCount > 0 {
		detail += fmt.Sprintf(" panic_lines=%d", patchSafety.PanicLineCount)
	}
	if len(patchSafety.Reasons) > 0 {
		detail += " reason=" + strings.Join(patchSafety.Reasons, "; ")
	}
	return detail
}

func severityForSafety(safety contractsv1.SafetyClassification) string {
	if safety == contractsv1.SafetyBlocked || safety == contractsv1.SafetyHighRisk || safety == contractsv1.SafetyCriticalRisk {
		return "warning"
	}
	return "info"
}

func severityForStatus(status contractsv1.RemediationStatus) string {
	switch status {
	case contractsv1.RemediationStatusSucceeded:
		return "info"
	case contractsv1.RemediationStatusRolledBack, contractsv1.RemediationStatusFailed:
		return "warning"
	default:
		return "info"
	}
}

func severityForOutcome(outcome string) string {
	if outcome == "succeeded" {
		return "info"
	}
	return "warning"
}

func firstPanicLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "panic(") {
			return line
		}
	}
	return ""
}

func safeExcerpt(content string) string {
	content = strings.ReplaceAll(content, "\x00", "")
	content = stripANSI(content)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) > 8 {
		lines = lines[:8]
	}
	excerpt := strings.Join(lines, "\n")
	if len(excerpt) > 1600 {
		return excerpt[:1600]
	}
	return excerpt
}

func snakeCase(value string) string {
	var out []rune
	var previousLower bool
	for index, char := range value {
		if unicode.IsUpper(char) {
			if index > 0 && previousLower {
				out = append(out, '_')
			}
			out = append(out, unicode.ToLower(char))
			previousLower = false
			continue
		}
		out = append(out, char)
		previousLower = unicode.IsLower(char) || unicode.IsDigit(char)
	}
	return string(out)
}

func stripANSI(content string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	return re.ReplaceAllString(content, "")
}
