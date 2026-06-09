package runtimev2_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/demoapp"
	runtimev2 "github.com/CloudSpaceLab/ai-logfixer/internal/runtime/v2"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store"
	"github.com/CloudSpaceLab/ai-logfixer/internal/workflow"
)

func TestRuntimeV2DetectsRepeated503AndAppliesConfigFix(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "app.json")
	logPath := filepath.Join(workDir, "app.log")

	if err := demoapp.WriteConfig(configPath, demoapp.Config{
		ServiceName: "goravel-demo",
		UpstreamURL: "http://127.0.0.1:1/orders",
	}); err != nil {
		t.Fatalf("write broken demo config: %v", err)
	}

	server := httptest.NewServer(demoapp.NewHandler(configPath, logPath))
	t.Cleanup(server.Close)

	for i := 0; i < 5; i++ {
		response, err := http.Get(server.URL + "/orders")
		if err != nil {
			t.Fatalf("request broken orders endpoint: %v", err)
		}
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected broken orders endpoint to return 503, got %d", response.StatusCode)
		}
		_ = response.Body.Close()
	}

	result, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:     "goravel-demo",
		BaseURL:         server.URL,
		LogPath:         logPath,
		ConfigPath:      configPath,
		HealthyUpstream: server.URL + "/upstream/orders",
		ErrorThreshold:  3,
		Now:             time.Date(2026, 5, 22, 9, 57, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run Runtime V2 process: %v", err)
	}

	if result.InvestigationRequest.SourceType != contractsv1.SourceTypeAutomatic {
		t.Fatalf("expected automatic investigation, got %q", result.InvestigationRequest.SourceType)
	}
	if result.Diagnosis.Status != contractsv1.DiagnosisStatusComplete {
		t.Fatalf("expected complete diagnosis, got %q", result.Diagnosis.Status)
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded remediation plan, got %q", result.RemediationPlan.Status)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded remediation attempt, got %q", result.Attempt.Status)
	}
	if result.Receipt.Outcome != "succeeded" {
		t.Fatalf("expected succeeded receipt, got %q", result.Receipt.Outcome)
	}
	if result.BackupPath == "" {
		t.Fatal("expected a backup path before patching config")
	}

	fixedConfig, err := demoapp.ReadConfig(configPath)
	if err != nil {
		t.Fatalf("read fixed demo config: %v", err)
	}
	if fixedConfig.UpstreamURL != server.URL+"/upstream/orders" {
		t.Fatalf("expected upstream to be fixed, got %q", fixedConfig.UpstreamURL)
	}

	response, err := http.Get(server.URL + "/orders")
	if err != nil {
		t.Fatalf("request fixed orders endpoint: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected fixed orders endpoint to return 200, got %d", response.StatusCode)
	}
}

func TestRuntimeV2RecordsRemediationPlanWorkflowTransitions(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "app.json")
	logPath := filepath.Join(workDir, "app.log")

	if err := demoapp.WriteConfig(configPath, demoapp.Config{
		ServiceName: "goravel-demo",
		UpstreamURL: "http://127.0.0.1:1/orders",
	}); err != nil {
		t.Fatalf("write broken demo config: %v", err)
	}

	server := httptest.NewServer(demoapp.NewHandler(configPath, logPath))
	t.Cleanup(server.Close)

	for i := 0; i < 5; i++ {
		response, err := http.Get(server.URL + "/orders")
		if err != nil {
			t.Fatalf("request broken orders endpoint: %v", err)
		}
		_ = response.Body.Close()
	}

	fakeStore := newFakeWorkflowStore()
	workflowService := workflow.NewService(fakeStore)
	workflowService.SetClock(func() time.Time {
		return time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	})
	workflowService.SetIDGenerator(newSequenceIDGenerator(
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	))

	result, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:           "goravel-demo",
		BaseURL:               server.URL,
		LogPath:               logPath,
		ConfigPath:            configPath,
		HealthyUpstream:       server.URL + "/upstream/orders",
		ErrorThreshold:        3,
		Now:                   time.Date(2026, 5, 22, 9, 57, 0, 0, time.UTC),
		WorkflowService:       workflowService,
		WorkflowTenantID:      "tenant-1",
		WorkflowActorID:       "ai-logfixer-v2-test",
		WorkflowCorrelationID: "corr-v2-1",
	})
	if err != nil {
		t.Fatalf("run Runtime V2 process with workflow service: %v", err)
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded remediation plan, got %s", result.RemediationPlan.Status)
	}
	if len(fakeStore.tx.remediationPlans.transitions) != 2 {
		t.Fatalf("expected two remediation plan transitions, got %+v", fakeStore.tx.remediationPlans.transitions)
	}
	first := fakeStore.tx.remediationPlans.transitions[0]
	if first.from != contractsv1.RemediationStatusApproved || first.to != contractsv1.RemediationStatusRunning {
		t.Fatalf("unexpected first transition: %+v", first)
	}
	second := fakeStore.tx.remediationPlans.transitions[1]
	if second.from != contractsv1.RemediationStatusRunning || second.to != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("unexpected second transition: %+v", second)
	}
	if len(fakeStore.tx.audit.events) != 2 {
		t.Fatalf("expected two audit events, got %+v", fakeStore.tx.audit.events)
	}
	if len(fakeStore.tx.outbox.events) != 2 {
		t.Fatalf("expected two outbox events, got %+v", fakeStore.tx.outbox.events)
	}
	expectedKey := "tenant-1:remediation_plan:" + result.RemediationPlan.ID + ":running:succeeded"
	if fakeStore.tx.outbox.events[1].IdempotencyKey != expectedKey {
		t.Fatalf("unexpected succeeded idempotency key: %s", fakeStore.tx.outbox.events[1].IdempotencyKey)
	}
}

func TestRuntimeV2RemediatesNestedJSONConfigForNonDemoRoute(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "checkout.json")
	logPath := filepath.Join(workDir, "checkout.log")
	writeJSONFile(t, configPath, map[string]any{
		"service_name": "checkout-api",
		"dependencies": map[string]any{
			"payment_url": "http://127.0.0.1:1/payments",
		},
	})

	server := httptest.NewServer(newCheckoutHandler(configPath, logPath))
	t.Cleanup(server.Close)

	for i := 0; i < 4; i++ {
		response, err := http.Get(server.URL + "/checkout")
		if err != nil {
			t.Fatalf("request broken checkout endpoint: %v", err)
		}
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected broken checkout endpoint to return 503, got %d", response.StatusCode)
		}
		_ = response.Body.Close()
	}

	result, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:      "checkout-api",
		LogPath:          logPath,
		ConfigPath:       configPath,
		Method:           http.MethodGet,
		Route:            "/checkout",
		StatusCode:       http.StatusServiceUnavailable,
		ConfigKeyPath:    "dependencies.payment_url",
		ReplacementValue: server.URL + "/upstream/payment",
		VerifyURL:        server.URL + "/checkout",
		ExpectedStatus:   http.StatusOK,
		ErrorThreshold:   3,
		Now:              time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run generic config remediation: %v", err)
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded remediation plan, got %q", result.RemediationPlan.Status)
	}
	if strings.Contains(result.RemediationPlan.ID, "goravel_upstream") {
		t.Fatalf("expected dynamic non-demo remediation plan ID, got %s", result.RemediationPlan.ID)
	}

	config := readJSONFile(t, configPath)
	dependencies := config["dependencies"].(map[string]any)
	if dependencies["payment_url"] != server.URL+"/upstream/payment" {
		t.Fatalf("expected nested payment URL to be fixed, got %+v", dependencies["payment_url"])
	}

	response, err := http.Get(server.URL + "/checkout")
	if err != nil {
		t.Fatalf("request fixed checkout endpoint: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected fixed checkout endpoint to return 200, got %d", response.StatusCode)
	}
}

func TestRuntimeV2MissingConfigPatchDescriptorEscalatesWithoutWriting(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "app.json")
	logPath := filepath.Join(workDir, "app.log")
	original := map[string]any{"upstream_url": "http://127.0.0.1:1/orders"}
	writeJSONFile(t, configPath, original)
	writeRepeatedKVFailures(t, logPath, "checkout-api", "/checkout", 4)

	result, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:    "checkout-api",
		LogPath:        logPath,
		ConfigPath:     configPath,
		Route:          "/checkout",
		StatusCode:     http.StatusServiceUnavailable,
		ErrorThreshold: 3,
		Now:            time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("expected blocked result without error, got %v", err)
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusEscalated || result.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked/escalated plan, got %+v", result.RemediationPlan)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusEscalated || result.Receipt.Outcome != "escalated" {
		t.Fatalf("expected escalated attempt and receipt, got attempt=%s receipt=%s", result.Attempt.Status, result.Receipt.Outcome)
	}
	if result.BackupPath != "" {
		t.Fatalf("blocked remediation should not create a backup, got %s", result.BackupPath)
	}
	if readJSONFile(t, configPath)["upstream_url"] != original["upstream_url"] {
		t.Fatal("blocked remediation changed the config file")
	}
}

func TestRuntimeV2NonConfigEvidenceEscalatesWithoutApplyingDescriptor(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "app.json")
	logPath := filepath.Join(workDir, "app.log")
	original := map[string]any{"upstream_url": "http://127.0.0.1:1/orders"}
	writeJSONFile(t, configPath, original)
	writeRepeatedFamilyFailures(t, logPath, "checkout-api", "/checkout", "permission_drift", 4)

	result, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:      "checkout-api",
		LogPath:          logPath,
		ConfigPath:       configPath,
		Route:            "/checkout",
		StatusCode:       http.StatusServiceUnavailable,
		ConfigKeyPath:    "upstream_url",
		ReplacementValue: "http://127.0.0.1:8090/upstream/orders",
		VerifyURL:        "http://127.0.0.1:8090/checkout",
		ErrorThreshold:   3,
		Now:              time.Date(2026, 5, 22, 12, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("expected blocked result without error, got %v", err)
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusEscalated || result.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected non-config evidence to block remediation, got %+v", result.RemediationPlan)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusEscalated || result.Receipt.Outcome != "escalated" {
		t.Fatalf("expected escalated attempt and receipt, got attempt=%s receipt=%s", result.Attempt.Status, result.Receipt.Outcome)
	}
	if !strings.Contains(result.RemediationPlan.UserMessage, "permission_drift") {
		t.Fatalf("expected block reason to include evidence family, got %q", result.RemediationPlan.UserMessage)
	}
	if result.BackupPath != "" {
		t.Fatalf("blocked remediation should not create a backup, got %s", result.BackupPath)
	}
	if readJSONFile(t, configPath)["upstream_url"] != original["upstream_url"] {
		t.Fatal("blocked remediation changed the config file")
	}
}

func TestRuntimeV2VerificationFailureRestoresConfig(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "checkout.json")
	logPath := filepath.Join(workDir, "checkout.log")
	originalURL := "http://127.0.0.1:1/payments"
	writeJSONFile(t, configPath, map[string]any{
		"service_name": "checkout-api",
		"dependencies": map[string]any{
			"payment_url": originalURL,
		},
	})

	server := httptest.NewServer(newCheckoutHandler(configPath, logPath))
	t.Cleanup(server.Close)
	for i := 0; i < 4; i++ {
		response, err := http.Get(server.URL + "/checkout")
		if err != nil {
			t.Fatalf("request broken checkout endpoint: %v", err)
		}
		_ = response.Body.Close()
	}

	result, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:      "checkout-api",
		LogPath:          logPath,
		ConfigPath:       configPath,
		Method:           http.MethodGet,
		Route:            "/checkout",
		StatusCode:       http.StatusServiceUnavailable,
		ConfigKeyPath:    "dependencies.payment_url",
		ReplacementValue: server.URL + "/upstream/payment",
		VerifyURL:        server.URL + "/checkout",
		ExpectedStatus:   http.StatusCreated,
		ErrorThreshold:   3,
		Now:              time.Date(2026, 5, 22, 13, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusFailed {
		t.Fatalf("expected failed remediation plan, got %+v", result.RemediationPlan)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusFailed {
		t.Fatalf("expected failed remediation attempt, got %+v", result.Attempt)
	}
	if result.Attempt.MonitorSummary.Status != "rolled_back" {
		t.Fatalf("expected rolled-back monitor summary, got %+v", result.Attempt.MonitorSummary)
	}
	if result.Receipt.Outcome != "failed_rolled_back" {
		t.Fatalf("expected failed rollback receipt, got %+v", result.Receipt)
	}
	if result.BackupPath == "" {
		t.Fatal("expected failed result to include backup path")
	}
	config := readJSONFile(t, configPath)
	dependencies := config["dependencies"].(map[string]any)
	if dependencies["payment_url"] != originalURL {
		t.Fatalf("expected config to be restored, got %+v", dependencies["payment_url"])
	}
}

func TestRuntimeV2RunsConfigPatchHookBeforeVerification(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "host", "app.json")
	containerConfigPath := filepath.Join(workDir, "container", "app.json")
	logPath := filepath.Join(workDir, "app.log")
	originalURL := "http://127.0.0.1:1/payments"
	for _, path := range []string{configPath, containerConfigPath} {
		writeJSONFile(t, path, map[string]any{
			"dependencies": map[string]any{
				"payment_url": originalURL,
			},
		})
	}

	server := httptest.NewServer(newCheckoutHandler(containerConfigPath, logPath))
	t.Cleanup(server.Close)
	for i := 0; i < 2; i++ {
		response, err := http.Get(server.URL + "/checkout")
		if err != nil {
			t.Fatalf("request broken checkout endpoint: %v", err)
		}
		_ = response.Body.Close()
	}

	hookCalled := false
	result, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:      "checkout-api",
		LogPath:          logPath,
		ConfigPath:       configPath,
		Method:           http.MethodGet,
		Route:            "/checkout",
		StatusClass:      http.StatusInternalServerError,
		ConfigKeyPath:    "dependencies.payment_url",
		ReplacementValue: server.URL + "/upstream/payment",
		VerifyURL:        server.URL + "/checkout",
		ExpectedStatus:   http.StatusOK,
		ErrorThreshold:   1,
		AfterConfigPatch: func(ctx context.Context, patch runtimev2.ConfigPatch) error {
			hookCalled = true
			if patch.ConfigPath != configPath || patch.ConfigKeyPath != "dependencies.payment_url" {
				t.Fatalf("unexpected patch descriptor: %+v", patch)
			}
			raw, err := os.ReadFile(patch.ConfigPath)
			if err != nil {
				return err
			}
			return os.WriteFile(containerConfigPath, raw, 0o644)
		},
	})
	if err != nil {
		t.Fatalf("run Runtime V2 with patch hook: %v", err)
	}
	if !hookCalled {
		t.Fatal("expected config patch hook to run")
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded remediation plan, got %s", result.RemediationPlan.Status)
	}
}

func TestRuntimeV2BelowThresholdDoesNotRemediate(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "app.json")
	logPath := filepath.Join(workDir, "app.log")
	writeJSONFile(t, configPath, map[string]any{"upstream_url": "http://127.0.0.1:1/orders"})
	writeRepeatedKVFailures(t, logPath, "checkout-api", "/checkout", 2)

	_, err := runtimev2.Run(context.Background(), runtimev2.Options{
		ServiceName:      "checkout-api",
		LogPath:          logPath,
		ConfigPath:       configPath,
		Route:            "/checkout",
		StatusCode:       http.StatusServiceUnavailable,
		ConfigKeyPath:    "upstream_url",
		ReplacementValue: "http://127.0.0.1:8090/upstream/orders",
		VerifyURL:        "http://127.0.0.1:8090/checkout",
		ErrorThreshold:   3,
		Now:              time.Date(2026, 5, 22, 14, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected threshold error")
	}
	if !strings.Contains(err.Error(), "failure threshold not reached") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func newCheckoutHandler(configPath string, logPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/upstream/payment", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/checkout", func(writer http.ResponseWriter, request *http.Request) {
		config := readJSONFileFromPath(configPath)
		dependencies, _ := config["dependencies"].(map[string]any)
		paymentURL, _ := dependencies["payment_url"].(string)
		response, err := http.Get(paymentURL)
		if err != nil {
			appendTestLog(logPath, "checkout-api", request.Method, "/checkout", http.StatusServiceUnavailable)
			http.Error(writer, "payment unavailable", http.StatusServiceUnavailable)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			appendTestLog(logPath, "checkout-api", request.Method, "/checkout", http.StatusServiceUnavailable)
			http.Error(writer, "payment unhealthy", http.StatusServiceUnavailable)
			return
		}
		appendTestLog(logPath, "checkout-api", request.Method, "/checkout", http.StatusOK)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})
	return mux
}

func writeRepeatedKVFailures(t *testing.T, path string, service string, route string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		appendTestLog(path, service, http.MethodGet, route, http.StatusServiceUnavailable)
	}
}

func writeRepeatedFamilyFailures(t *testing.T, path string, service string, route string, family string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		appendTestLogWithFields(path, service, http.MethodGet, route, http.StatusServiceUnavailable, map[string]string{
			"family": family,
			"error":  "permission denied writing runtime path",
		})
	}
}

func appendTestLog(path string, service string, method string, route string, status int) {
	appendTestLogWithFields(path, service, method, route, status, nil)
}

func appendTestLogWithFields(path string, service string, method string, route string, status int, fields map[string]string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	line := time.Now().UTC().Format(time.RFC3339Nano) + " level=error service=" + service + " method=" + method + " route=" + route + " status=" + intString(status)
	for key, value := range fields {
		line += " " + key + "=" + strconv.Quote(value)
	}
	line += "\n"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line)
}

func writeJSONFile(t *testing.T, path string, value map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write json file: %v", err)
	}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	return readJSONFileFromPath(path)
}

func readJSONFileFromPath(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func intString(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}

type fakeWorkflowStore struct {
	tx *fakeWorkflowTx
}

func newFakeWorkflowStore() *fakeWorkflowStore {
	return &fakeWorkflowStore{tx: &fakeWorkflowTx{}}
}

func (s *fakeWorkflowStore) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return fn(ctx, s.tx)
}

type fakeWorkflowTx struct {
	remediationPlans fakeRemediationPlanRepository
	audit            fakeAuditRepository
	outbox           fakeOutboxRepository
}

func (t *fakeWorkflowTx) InvestigationRequests() store.InvestigationRequestRepository {
	return nil
}

func (t *fakeWorkflowTx) InvestigationClusters() store.InvestigationClusterRepository {
	return nil
}

func (t *fakeWorkflowTx) InvestigationBranches() store.InvestigationBranchRepository {
	return nil
}

func (t *fakeWorkflowTx) DiagnosisResults() store.DiagnosisResultRepository {
	return nil
}

func (t *fakeWorkflowTx) RemediationPlans() store.RemediationPlanRepository {
	return &t.remediationPlans
}

func (t *fakeWorkflowTx) ApprovalRequests() store.ApprovalRequestRepository {
	return nil
}

func (t *fakeWorkflowTx) RemediationAttempts() store.RemediationAttemptRepository {
	return nil
}

func (t *fakeWorkflowTx) Receipts() store.ReceiptRepository {
	return nil
}

func (t *fakeWorkflowTx) AuditEvents() store.AuditEventRepository {
	return &t.audit
}

func (t *fakeWorkflowTx) WorkflowLeases() store.WorkflowLeaseRepository {
	return nil
}

func (t *fakeWorkflowTx) OutboxEvents() store.OutboxEventRepository {
	return &t.outbox
}

type remediationTransition struct {
	tenantID string
	id       string
	from     contractsv1.RemediationStatus
	to       contractsv1.RemediationStatus
}

type fakeRemediationPlanRepository struct {
	transitions []remediationTransition
}

func (r *fakeRemediationPlanRepository) Create(context.Context, store.ContractRecord[contractsv1.RemediationPlan]) error {
	return nil
}

func (r *fakeRemediationPlanRepository) Get(context.Context, string, string) (store.ContractRecord[contractsv1.RemediationPlan], error) {
	return store.ContractRecord[contractsv1.RemediationPlan]{}, nil
}

func (r *fakeRemediationPlanRepository) UpdateStatus(_ context.Context, tenantID string, id string, from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) error {
	r.transitions = append(r.transitions, remediationTransition{tenantID: tenantID, id: id, from: from, to: to})
	return nil
}

type fakeAuditRepository struct {
	events []store.AuditEvent
}

func (r *fakeAuditRepository) Append(_ context.Context, event store.AuditEvent) error {
	r.events = append(r.events, event)
	return nil
}

type fakeOutboxRepository struct {
	events []store.OutboxEvent
}

func (r *fakeOutboxRepository) Append(_ context.Context, event store.OutboxEvent) error {
	r.events = append(r.events, event)
	return nil
}

func (r *fakeOutboxRepository) ClaimDue(context.Context, string, string, int, time.Time) ([]store.OutboxEvent, error) {
	return nil, nil
}

func (r *fakeOutboxRepository) MarkPublished(context.Context, string, string, time.Time) error {
	return nil
}

func (r *fakeOutboxRepository) MarkFailed(context.Context, string, string, time.Time, string) error {
	return nil
}

func newSequenceIDGenerator(values ...string) workflow.IDGenerator {
	index := 0
	return func() (string, error) {
		if index >= len(values) {
			return values[len(values)-1], nil
		}
		value := values[index]
		index++
		return value, nil
	}
}
