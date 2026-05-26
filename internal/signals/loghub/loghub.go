package loghub

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
)

type Format string

const (
	FormatApache    Format = "apache"
	FormatOpenStack Format = "openstack"
)

type Options struct {
	ServiceName string
	SourceName  string
	LogPath     string
	Format      Format
	Threshold   int
	Window      time.Duration
	Now         time.Time
}

type Analysis struct {
	Signal               engine.IncidentSignal
	InvestigationRequest contractsv1.InvestigationRequest
	Diagnosis            contractsv1.DiagnosisResult
	RemediationPlan      contractsv1.RemediationPlan
	Attempt              contractsv1.RemediationAttempt
	Receipt              contractsv1.Receipt
}

type event struct {
	Level     string
	Message   string
	Signature string
	Raw       string
}

func Analyze(content string, options Options) (Analysis, error) {
	options = normalizeOptions(options)
	events := parseEvents(content, options.Format)
	signal, ok := strongestSignal(events, options)
	if !ok {
		return Analysis{}, fmt.Errorf("Loghub %s threshold not reached for %s", options.Format, options.ServiceName)
	}

	factory := engine.NewContractIDFactory()
	investigation := buildInvestigationRequest(options, signal, factory)
	diagnosis := buildDiagnosis(options, signal, factory)
	builder := engine.BlockedPlanBuilder{IDFactory: factory, Now: options.Now, Actor: "loghub-corpus-adapter"}
	reason := "Automatic remediation is blocked because corpus logs do not identify a local source owner or safe allowlisted patch."
	plan := builder.RemediationPlan(diagnosis.ID, signal, reason)
	attempt := builder.EscalatedAttempt(plan.ID, signal, reason)
	receipt := builder.EscalatedReceipt(diagnosis.ID, plan.ID, attempt.ID, signal, reason)

	if err := investigation.Validate(); err != nil {
		return Analysis{}, fmt.Errorf("validate investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return Analysis{}, fmt.Errorf("validate diagnosis: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return Analysis{}, fmt.Errorf("validate remediation plan: %w", err)
	}
	if err := attempt.Validate(); err != nil {
		return Analysis{}, fmt.Errorf("validate remediation attempt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return Analysis{}, fmt.Errorf("validate receipt: %w", err)
	}

	return Analysis{
		Signal:               signal,
		InvestigationRequest: investigation,
		Diagnosis:            diagnosis,
		RemediationPlan:      plan,
		Attempt:              attempt,
		Receipt:              receipt,
	}, nil
}

func normalizeOptions(options Options) Options {
	if options.ServiceName == "" {
		options.ServiceName = "unknown-loghub-service"
	}
	if options.SourceName == "" {
		options.SourceName = "loghub-corpus-adapter"
	}
	if options.Format == "" {
		options.Format = FormatApache
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
	return options
}

func parseEvents(content string, format Format) []event {
	var out []event
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		parsed, err := parseEvent(line, format)
		if err != nil {
			continue
		}
		out = append(out, parsed)
	}
	return out
}

func parseEvent(line string, format Format) (event, error) {
	switch format {
	case FormatApache:
		re := regexp.MustCompile(`^\[[^\]]+\]\s+\[([^\]]+)\]\s+(.*)$`)
		match := re.FindStringSubmatch(line)
		if len(match) != 3 {
			return event{}, errors.New("not an Apache log line")
		}
		level := strings.ToLower(match[1])
		if !strings.Contains(level, "error") && !strings.Contains(level, "warn") && !strings.Contains(level, "crit") {
			return event{}, errors.New("not an error-like Apache log line")
		}
		message := strings.TrimSpace(match[2])
		return event{Level: level, Message: message, Signature: normalizeSignature(message), Raw: line}, nil
	case FormatOpenStack:
		upper := strings.ToUpper(line)
		if !strings.Contains(upper, "ERROR") && !strings.Contains(upper, "TRACE") && !strings.Contains(line, "HTTP exception thrown:") && !strings.Contains(line, "Exception") {
			return event{}, errors.New("not an error-like OpenStack log line")
		}
		message := line
		if index := strings.Index(line, "HTTP exception thrown:"); index >= 0 {
			message = line[index:]
		}
		return event{Level: "error", Message: message, Signature: normalizeSignature(message), Raw: line}, nil
	default:
		return event{}, fmt.Errorf("unsupported Loghub format %q", format)
	}
}

func strongestSignal(events []event, options Options) (engine.IncidentSignal, bool) {
	bySignature := map[string][]event{}
	for _, item := range events {
		bySignature[item.Signature] = append(bySignature[item.Signature], item)
	}
	var signatures []string
	for signature := range bySignature {
		signatures = append(signatures, signature)
	}
	sort.Slice(signatures, func(i, j int) bool {
		left := bySignature[signatures[i]]
		right := bySignature[signatures[j]]
		if len(left) != len(right) {
			return len(left) > len(right)
		}
		return signatures[i] < signatures[j]
	})
	for _, signature := range signatures {
		items := bySignature[signature]
		if len(items) < options.Threshold {
			continue
		}
		start := options.Now.Add(-options.Window)
		end := options.Now
		return engine.IncidentSignal{
			Service:     options.ServiceName,
			Source:      options.LogPath,
			Kind:        "loghub_" + string(options.Format),
			Code:        string(options.Format) + ":error_signature",
			Signature:   signature,
			Count:       len(items),
			Start:       start,
			End:         end,
			RawExcerpts: loghubExcerpts(items, 8),
			Tags:        []string{"loghub", string(options.Format), "corpus"},
		}, true
	}
	return engine.IncidentSignal{}, false
}

func buildInvestigationRequest(options Options, signal engine.IncidentSignal, factory engine.ContractIDFactory) contractsv1.InvestigationRequest {
	return contractsv1.InvestigationRequest{
		ID:              factory.ID("inv_req_loghub_signature", signal.StableParts()...),
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      options.SourceName,
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         "Repeated real-world log signature detected",
		ErrorCode:       signal.ErrorCode(),
		TimeWindow:      contractsv1.TimeWindow{Start: signal.Start, End: signal.End},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "repeated_log_signature",
			ErrorCode:     signal.ErrorCode(),
			Source:        options.LogPath,
			DeployVersion: "corpus",
			Tags:          signal.Tags,
		},
		DisplayStatus: "Corpus-backed log signature detected",
		UserMessage:   "I detected a repeated real-world log signature and started a safety-bounded investigation.",
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildDiagnosis(options Options, signal engine.IncidentSignal, factory engine.ContractIDFactory) contractsv1.DiagnosisResult {
	parts := signal.StableParts()
	return contractsv1.DiagnosisResult{
		ID:                 factory.ID("diag_loghub_signature", parts...),
		ContractVersion:    contractsv1.ContractVersion,
		SchemaURL:          contractsv1.DiagnosisSchemaURL,
		Status:             contractsv1.DiagnosisStatusComplete,
		Summary:            "Repeated corpus log signature requires source-owner review.",
		Confidence:         0.76,
		SuspectedRootCause: "The logs show a repeated error signature, but corpus data does not provide local source ownership or a safe patch target.",
		AffectedServices:   []string{options.ServiceName},
		EvidenceItems: []contractsv1.EvidenceItem{
			{
				ID:             factory.ID("ev_loghub_signature", parts...),
				Type:           contractsv1.EvidenceTypeLog,
				Source:         options.LogPath,
				Timestamp:      options.Now,
				Title:          "Repeated corpus log signature",
				Summary:        fmt.Sprintf("The %s corpus excerpt contains %d repeats of signature %q.", options.Format, signal.Count, signal.Signature),
				RawExcerpt:     strings.Join(signal.RawExcerpts, "\n"),
				RedactionState: contractsv1.RedactionStateRedacted,
				RelatedIDs:     []string{},
				UIHints:        contractsv1.UIHints{Icon: "file-warning", Tone: "warning", Sections: []string{"logs", "evidence"}},
				ExternalRefs:   []contractsv1.ExternalRef{},
				KnowledgeRefs:  []contractsv1.KnowledgeRef{},
			},
		},
		Recommendations: []contractsv1.RunbookRecommendation{
			{
				ID:                  factory.ID("rec_loghub_manual_review", parts...),
				Title:               "Escalate corpus signature for service-owner review",
				Reason:              "The signature is real-world evidence, but no local route, source file, config key, or rollback target is available.",
				Confidence:          0.74,
				Steps:               []string{"Review the grouped log signature.", "Identify the owning service/source path.", "Choose an allowlisted remediator or perform a manual patch.", "Verify the error signature stops recurring."},
				RequiredPermissions: []string{"logs:read", "service_owner:required", "manual_patch:required"},
				EstimatedRisk:       contractsv1.SafetyBlocked,
				RequiresApproval:    true,
			},
		},
		PatchPlan:            nil,
		RollbackPlan:         nil,
		SafetyClassification: contractsv1.SafetyBlocked,
		DisplayStatus:        "Automatic fix blocked",
		UserMessage:          "I grouped the real-world log signature and left the target unchanged because no safe patch target is known.",
		NextActions:          []contractsv1.NextAction{{ID: "next_identify_source_owner", Label: "Identify owner", ActionType: "manual_review", Description: "Map the signature to an owned service, source path, config, or runbook.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_loghub_diag", parts...), Type: "diagnosis.completed", Message: "Corpus signature diagnosis completed without automatic patch.", Severity: "warning", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
}

func normalizeSignature(message string) string {
	message = strings.TrimSpace(message)
	uuidRe := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	message = uuidRe.ReplaceAllString(message, "<uuid>")
	ipRe := regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	message = ipRe.ReplaceAllString(message, "<ip>")
	spaceRe := regexp.MustCompile(`\s+`)
	return spaceRe.ReplaceAllString(message, " ")
}

func loghubExcerpts(items []event, limit int) []string {
	if limit <= 0 || len(items) <= limit {
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, item.Raw)
		}
		return out
	}
	return loghubExcerpts(items[len(items)-limit:], limit)
}
