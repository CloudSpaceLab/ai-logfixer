package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
	"github.com/CloudSpaceLab/ai-logfixer/internal/intake"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store/postgres"
)

type output struct {
	Status  string         `json:"status"`
	Mode    string         `json:"mode"`
	Message string         `json:"message"`
	Signals []signalOutput `json:"signals"`
	Results []resultOutput `json:"results,omitempty"`
}

type signalOutput struct {
	Service     string    `json:"service"`
	Source      string    `json:"source"`
	Kind        string    `json:"kind"`
	Method      string    `json:"method,omitempty"`
	Route       string    `json:"route,omitempty"`
	StatusCode  int       `json:"status_code,omitempty"`
	StatusClass int       `json:"status_class,omitempty"`
	ErrorCode   string    `json:"error_code"`
	Signature   string    `json:"signature,omitempty"`
	Count       int       `json:"count"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Tags        []string  `json:"tags"`
}

type resultOutput struct {
	Signal               signalOutput                      `json:"signal"`
	InvestigationRequest contractsv1.InvestigationRequest  `json:"investigation_request"`
	Cluster              contractsv1.InvestigationCluster  `json:"cluster"`
	Branch               contractsv1.InvestigationBranch   `json:"branch"`
	Decision             contractsv1.InvestigationDecision `json:"decision"`
}

type options struct {
	logPath        string
	serviceName    string
	sourceName     string
	method         string
	route          string
	statusCode     int
	statusClass    int
	threshold      int
	window         time.Duration
	now            time.Time
	persist        bool
	postgresDSN    string
	tenantID       string
	environmentID  string
	serviceID      string
	requestedBy    string
	actorID        string
	correlationID  string
	idempotencyKey string
	suppressOutbox bool
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "detect: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(options.logPath)
	if err != nil {
		return fmt.Errorf("read log: %w", err)
	}

	entries := engine.ParseKeyValueHTTPLogs(string(content), options.logPath)
	signals := engine.RepeatedHTTPFailures(entries, engine.FailureThreshold{
		ServiceName: options.serviceName,
		Method:      options.method,
		Route:       options.route,
		StatusCode:  options.statusCode,
		StatusClass: options.statusClass,
		MinCount:    options.threshold,
		Window:      options.window,
	})
	if len(signals) == 0 {
		return writeJSON(stdout, output{
			Status:  "no_signal",
			Mode:    mode(options.persist),
			Message: "failure threshold not reached",
			Signals: []signalOutput{},
		})
	}

	results, err := runIntake(ctx, options, signals)
	if err != nil {
		return err
	}
	return writeJSON(stdout, output{
		Status:  "started",
		Mode:    mode(options.persist),
		Message: fmt.Sprintf("started %d investigation intake record(s)", len(results)),
		Signals: signalOutputs(signals),
		Results: results,
	})
}

func parseOptions(args []string) (options, error) {
	var parsed options
	var window string
	var now string
	flags := flag.NewFlagSet("ai-logfixer-detect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.logPath, "log", "", "path to key-value HTTP log file")
	flags.StringVar(&parsed.serviceName, "service", "", "service name")
	flags.StringVar(&parsed.sourceName, "source-name", "http-log-detector", "detector source name")
	flags.StringVar(&parsed.method, "method", "", "HTTP method filter")
	flags.StringVar(&parsed.route, "route", "", "route filter")
	flags.IntVar(&parsed.statusCode, "status", 0, "exact HTTP status filter")
	flags.IntVar(&parsed.statusClass, "status-class", 500, "HTTP status class filter")
	flags.IntVar(&parsed.threshold, "threshold", 3, "minimum repeated failures")
	flags.StringVar(&window, "window", "5m", "failure grouping window")
	flags.StringVar(&now, "now", "", "override current time as RFC3339")
	flags.BoolVar(&parsed.persist, "persist", false, "persist intake records to PostgreSQL")
	flags.StringVar(&parsed.postgresDSN, "postgres-dsn", "", "PostgreSQL DSN for durable mode")
	flags.StringVar(&parsed.tenantID, "tenant-id", "", "tenant id for durable records")
	flags.StringVar(&parsed.environmentID, "environment-id", "", "environment id for durable records")
	flags.StringVar(&parsed.serviceID, "service-id", "", "service id for durable records")
	flags.StringVar(&parsed.requestedBy, "requested-by", "ai-logfixer", "requesting actor")
	flags.StringVar(&parsed.actorID, "actor-id", "", "audit actor id")
	flags.StringVar(&parsed.correlationID, "correlation-id", "", "correlation id")
	flags.StringVar(&parsed.idempotencyKey, "idempotency-key", "", "base idempotency key")
	flags.BoolVar(&parsed.suppressOutbox, "suppress-outbox", false, "suppress outbox event emission")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if strings.TrimSpace(parsed.logPath) == "" {
		return options{}, errors.New("log path is required")
	}
	if strings.TrimSpace(parsed.serviceName) == "" {
		return options{}, errors.New("service is required")
	}
	duration, err := time.ParseDuration(window)
	if err != nil {
		return options{}, fmt.Errorf("parse window: %w", err)
	}
	parsed.window = duration
	if now != "" {
		parsed.now, err = time.Parse(time.RFC3339, now)
		if err != nil {
			return options{}, fmt.Errorf("parse now: %w", err)
		}
	}
	if parsed.statusCode > 0 {
		parsed.statusClass = 0
	}
	if parsed.actorID == "" {
		parsed.actorID = parsed.requestedBy
	}
	if parsed.persist {
		if strings.TrimSpace(parsed.postgresDSN) == "" {
			return options{}, errors.New("postgres-dsn is required when persist is true")
		}
		if strings.TrimSpace(parsed.tenantID) == "" || strings.TrimSpace(parsed.environmentID) == "" || strings.TrimSpace(parsed.serviceID) == "" {
			return options{}, errors.New("tenant-id, environment-id, and service-id are required when persist is true")
		}
	} else {
		parsed.tenantID = defaultString(parsed.tenantID, "dry-run-tenant")
		parsed.environmentID = defaultString(parsed.environmentID, "dry-run-environment")
		parsed.serviceID = defaultString(parsed.serviceID, "dry-run-service-"+sanitizeID(parsed.serviceName))
	}
	return parsed, nil
}

func runIntake(ctx context.Context, options options, signals []engine.IncidentSignal) ([]resultOutput, error) {
	if options.persist {
		db, err := sql.Open("pgx", options.postgresDSN)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		defer db.Close()
		if err := db.PingContext(ctx); err != nil {
			return nil, fmt.Errorf("ping postgres: %w", err)
		}
		service := intake.NewService(postgres.New(db))
		if !options.now.IsZero() {
			service.SetClock(func() time.Time { return options.now })
		}
		return persistSignals(ctx, service, options, signals)
	}

	service := intake.NewService(nil)
	if !options.now.IsZero() {
		service.SetClock(func() time.Time { return options.now })
	}
	results := make([]resultOutput, 0, len(signals))
	for index, signal := range signals {
		startResult, err := service.PlanStartNewInvestigation(startInput(options, signal, index, len(signals)))
		if err != nil {
			return nil, err
		}
		results = append(results, resultOutputFrom(startResult))
	}
	return results, nil
}

func persistSignals(ctx context.Context, service *intake.Service, options options, signals []engine.IncidentSignal) ([]resultOutput, error) {
	results := make([]resultOutput, 0, len(signals))
	for index, signal := range signals {
		startResult, err := service.StartNewInvestigation(ctx, startInput(options, signal, index, len(signals)))
		if err != nil {
			return nil, err
		}
		results = append(results, resultOutputFrom(startResult))
	}
	return results, nil
}

func startInput(options options, signal engine.IncidentSignal, index int, total int) intake.StartInput {
	idempotencyKey := options.idempotencyKey
	if idempotencyKey != "" && total > 1 {
		idempotencyKey = idempotencyKey + "-" + strconv.Itoa(index+1)
	}
	correlationID := options.correlationID
	if correlationID == "" {
		correlationID = idempotencyKey
	}
	return intake.StartInput{
		TenantID:       options.tenantID,
		EnvironmentID:  options.environmentID,
		ServiceID:      options.serviceID,
		ServiceName:    options.serviceName,
		SourceName:     options.sourceName,
		RequestedBy:    options.requestedBy,
		ActorID:        options.actorID,
		CorrelationID:  correlationID,
		IdempotencyKey: idempotencyKey,
		SuppressOutbox: options.suppressOutbox,
		Signal:         signal,
	}
}

func resultOutputFrom(result intake.StartResult) resultOutput {
	return resultOutput{
		Signal:               signalOutputFrom(result.Signal),
		InvestigationRequest: result.InvestigationRequest,
		Cluster:              result.Cluster,
		Branch:               result.Branch,
		Decision:             result.Decision,
	}
}

func signalOutputs(signals []engine.IncidentSignal) []signalOutput {
	out := make([]signalOutput, 0, len(signals))
	for _, signal := range signals {
		out = append(out, signalOutputFrom(signal))
	}
	return out
}

func signalOutputFrom(signal engine.IncidentSignal) signalOutput {
	return signalOutput{
		Service:     signal.Service,
		Source:      signal.Source,
		Kind:        signal.Kind,
		Method:      signal.Method,
		Route:       signal.Route,
		StatusCode:  signal.StatusCode,
		StatusClass: signal.StatusClass,
		ErrorCode:   signal.ErrorCode(),
		Signature:   signal.Signature,
		Count:       signal.Count,
		Start:       signal.Start,
		End:         signal.End,
		Tags:        signal.Tags,
	}
}

func writeJSON(writer io.Writer, value output) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func mode(persist bool) string {
	if persist {
		return "persisted"
	}
	return "dry_run"
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func sanitizeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}
