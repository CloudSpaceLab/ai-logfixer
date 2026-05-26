package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store"
	"github.com/CloudSpaceLab/ai-logfixer/internal/workflow"
)

const postgresDSNEnv = "AILOGFIXER_POSTGRES_DSN"

func TestPostgresStoreIntegration(t *testing.T) {
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", postgresDSNEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin postgres connection: %v", err)
	}
	defer adminDB.Close()

	schemaName := "ailogfixer_it_" + randomHex(t, 6)
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+quoteIdent(schemaName)); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = adminDB.ExecContext(dropCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schemaName)+" CASCADE")
	})

	db, err := sql.Open("pgx", withSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("open schema-scoped postgres connection: %v", err)
	}
	defer db.Close()

	applyMigration(t, ctx, db)

	tenantID, environmentID, serviceID := seedService(t, ctx, db)
	workflowStore := New(db)
	now := time.Date(2026, 5, 25, 14, 0, 0, 0, time.UTC)

	if _, err := db.ExecContext(ctx, `
INSERT INTO rollback_plans (id, tenant_id, contract_version, strategy, availability, created_at, payload_json)
VALUES ($1, $2, 'v1', $3, $4, $5, $6)`,
		"rollback-it-1",
		tenantID,
		"restore_config",
		"available",
		now,
		`{"id":"rollback-it-1","rollback_type":"restore_config","restore_steps":["restore snapshot"],"limitations":[],"risk_level":"low_risk","requires_manual_review":false}`,
	); err != nil {
		t.Fatalf("insert rollback plan: %v", err)
	}

	if err := workflowStore.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.InvestigationRequests().Create(ctx, store.ContractRecord[contractsv1.InvestigationRequest]{
			TenantID:       tenantID,
			EnvironmentID:  environmentID,
			ServiceID:      serviceID,
			Status:         string(contractsv1.InvestigationStatusRequested),
			IdempotencyKey: "req-it-1",
			Payload: contractsv1.InvestigationRequest{
				ID:              "req-it-1",
				ContractVersion: contractsv1.ContractVersion,
				SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
				SourceType:      contractsv1.SourceTypeAutomatic,
				SourceName:      "integration-test",
				Service:         "checkout-api",
				Symptom:         "HTTP 500",
				ErrorCode:       "500",
				TimeWindow: contractsv1.TimeWindow{
					Start: now.Add(-5 * time.Minute),
					End:   now,
				},
				SignalFingerprint: contractsv1.SignalFingerprint{
					Service: "checkout-api",
					Symptom: "HTTP 500",
					Source:  "integration-test",
				},
				DisplayStatus: "Requested",
				UserMessage:   "Investigating repeated 500s.",
				CreatedAt:     now,
			},
		}); err != nil {
			return err
		}
		if err := tx.InvestigationClusters().Create(ctx, store.ContractRecord[contractsv1.InvestigationCluster]{
			TenantID:      tenantID,
			EnvironmentID: environmentID,
			ServiceID:     serviceID,
			Status:        string(contractsv1.InvestigationStatusRunning),
			Payload: contractsv1.InvestigationCluster{
				ID:             "cluster-it-1",
				Status:         contractsv1.InvestigationStatusRunning,
				PrimaryService: "checkout-api",
				Summary:        "Repeated checkout failures",
				CreatedAt:      now,
				UpdatedAt:      now,
			},
		}); err != nil {
			return err
		}
		if err := tx.InvestigationBranches().Create(ctx, store.ContractRecord[contractsv1.InvestigationBranch]{
			TenantID:      tenantID,
			EnvironmentID: environmentID,
			ServiceID:     serviceID,
			Status:        string(contractsv1.InvestigationStatusRunning),
			Relations: store.ContractRelations{
				ClusterID: "cluster-it-1",
				RequestID: "req-it-1",
			},
			Payload: contractsv1.InvestigationBranch{
				ID:               "branch-it-1",
				ClusterID:        "cluster-it-1",
				BranchType:       "primary",
				Symptom:          "HTTP 500",
				Status:           contractsv1.InvestigationStatusRunning,
				SourceRequestIDs: []string{"req-it-1"},
				DisplayStatus:    "Running",
				UserMessage:      "Collecting evidence.",
				CreatedAt:        now,
				UpdatedAt:        now,
			},
		}); err != nil {
			return err
		}
		if err := tx.DiagnosisResults().Create(ctx, store.ContractRecord[contractsv1.DiagnosisResult]{
			TenantID:      tenantID,
			EnvironmentID: environmentID,
			ServiceID:     serviceID,
			Status:        string(contractsv1.DiagnosisStatusComplete),
			Relations: store.ContractRelations{
				BranchID: "branch-it-1",
			},
			Payload: contractsv1.DiagnosisResult{
				ID:                   "diag-it-1",
				ContractVersion:      contractsv1.ContractVersion,
				SchemaURL:            contractsv1.DiagnosisSchemaURL,
				Status:               contractsv1.DiagnosisStatusComplete,
				Summary:              "Checkout is failing because a dependency is unavailable.",
				Confidence:           0.9,
				SuspectedRootCause:   "Dependency unavailable",
				AffectedServices:     []string{"checkout-api"},
				SafetyClassification: contractsv1.SafetyLowRisk,
				DisplayStatus:        "Complete",
				UserMessage:          "Found the likely dependency issue.",
				CreatedAt:            now,
			},
		}); err != nil {
			return err
		}
		return tx.RemediationPlans().Create(ctx, store.ContractRecord[contractsv1.RemediationPlan]{
			TenantID:      tenantID,
			EnvironmentID: environmentID,
			ServiceID:     serviceID,
			Status:        string(contractsv1.RemediationStatusPlanning),
			Relations: store.ContractRelations{
				DiagnosisResultID: "diag-it-1",
				RollbackPlanID:    "rollback-it-1",
			},
			Payload: contractsv1.RemediationPlan{
				ID:                "plan-it-1",
				ContractVersion:   contractsv1.ContractVersion,
				SchemaURL:         contractsv1.RemediationPlanSchemaURL,
				DiagnosisResultID: "diag-it-1",
				Summary:           "Restore checkout dependency configuration.",
				FixPreview: contractsv1.DiffPreview{
					Before: "bad upstream",
					After:  "healthy upstream",
				},
				RollbackPlan: contractsv1.RollbackPlan{
					ID:                   "rollback-it-1",
					RollbackType:         contractsv1.RollbackRestoreConfig,
					RestoreSteps:         []string{"restore snapshot"},
					RiskLevel:            contractsv1.SafetyLowRisk,
					RequiresManualReview: false,
				},
				RiskLevel:        contractsv1.SafetyLowRisk,
				ApprovalRequired: false,
				Status:           contractsv1.RemediationStatusPlanning,
				DisplayStatus:    "Planning",
				UserMessage:      "Preparing a low-risk remediation.",
				CreatedAt:        now,
			},
		})
	}); err != nil {
		t.Fatalf("create workflow records: %v", err)
	}

	gotPlan := getRemediationPlan(t, ctx, workflowStore, tenantID, "plan-it-1")
	if gotPlan.Payload.ID != "plan-it-1" || gotPlan.Status != string(contractsv1.RemediationStatusPlanning) {
		t.Fatalf("unexpected remediation plan before transition: %+v", gotPlan)
	}

	svc := workflow.NewService(workflowStore)
	svc.SetClock(func() time.Time { return now.Add(time.Minute) })
	svc.SetIDGenerator(func() (string, error) {
		return "33333333-3333-4333-8333-333333333333", nil
	})
	if err := svc.MoveRemediation(ctx, workflow.RemediationTransition{
		TenantID:     tenantID,
		ResourceType: workflow.ResourceRemediationPlan,
		ResourceID:   "plan-it-1",
		From:         contractsv1.RemediationStatusPlanning,
		To:           contractsv1.RemediationStatusAwaitingApproval,
		Metadata: workflow.TransitionMetadata{
			ActorID:       "integration-test",
			CorrelationID: "corr-it-1",
		},
	}); err != nil {
		t.Fatalf("move remediation through workflow service: %v", err)
	}

	gotPlan = getRemediationPlan(t, ctx, workflowStore, tenantID, "plan-it-1")
	if gotPlan.Status != string(contractsv1.RemediationStatusAwaitingApproval) {
		t.Fatalf("expected transitioned status, got %s", gotPlan.Status)
	}
	if gotPlan.LockVersion != 1 {
		t.Fatalf("expected lock_version 1 after transition, got %d", gotPlan.LockVersion)
	}

	assertCount(t, ctx, db, `
SELECT count(*)
FROM audit_events
WHERE tenant_id = $1 AND resource_type = $2 AND resource_id = $3 AND before_state = $4 AND after_state = $5`,
		tenantID,
		workflow.ResourceRemediationPlan,
		"plan-it-1",
		string(contractsv1.RemediationStatusPlanning),
		string(contractsv1.RemediationStatusAwaitingApproval),
		1,
	)

	if err := workflowStore.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		events, err := tx.OutboxEvents().ClaimDue(ctx, tenantID, "worker-it-1", 10, now.Add(2*time.Minute))
		if err != nil {
			return err
		}
		if len(events) != 1 {
			return fmt.Errorf("expected one claimed outbox event, got %d", len(events))
		}
		if events[0].ID != "33333333-3333-4333-8333-333333333333" {
			return fmt.Errorf("unexpected outbox id %s", events[0].ID)
		}
		return tx.OutboxEvents().MarkPublished(ctx, tenantID, events[0].ID, now.Add(3*time.Minute))
	}); err != nil {
		t.Fatalf("claim and publish outbox event: %v", err)
	}

	if err := workflowStore.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lease, err := tx.WorkflowLeases().Acquire(ctx, store.WorkflowLease{
			ResourceType: workflow.ResourceRemediationPlan,
			ResourceID:   "plan-it-1",
			OwnerID:      "worker-it-1",
			ExpiresAt:    now.Add(5 * time.Minute),
		})
		if err != nil {
			return err
		}
		if lease.FencingToken != 1 {
			return fmt.Errorf("expected initial fencing token 1, got %d", lease.FencingToken)
		}
		renewed, err := tx.WorkflowLeases().Renew(ctx, lease, now.Add(time.Minute), 5*time.Minute)
		if err != nil {
			return err
		}
		if renewed.FencingToken != lease.FencingToken {
			return fmt.Errorf("expected renew to keep fencing token %d, got %d", lease.FencingToken, renewed.FencingToken)
		}
		return tx.WorkflowLeases().Release(ctx, renewed)
	}); err != nil {
		t.Fatalf("acquire renew and release workflow lease: %v", err)
	}
}

func applyMigration(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	migrationPath := filepath.Join("..", "..", "..", "db", "migrations", "postgres", "0001_workflow_store.sql")
	raw, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration %s: %v", migrationPath, err)
	}
	for _, statement := range splitSQLStatements(string(raw)) {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("apply migration statement:\n%s\nerror: %v", statement, err)
		}
	}
}

func splitSQLStatements(raw string) []string {
	var statements []string
	for _, statement := range strings.Split(raw, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

func seedService(t *testing.T, ctx context.Context, db *sql.DB) (string, string, string) {
	t.Helper()

	var tenantID string
	if err := db.QueryRowContext(ctx, `
INSERT INTO tenants (name, slug)
VALUES ('Integration Tenant', 'integration-tenant')
RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	var environmentID string
	if err := db.QueryRowContext(ctx, `
INSERT INTO environments (tenant_id, name, kind)
VALUES ($1, 'test', 'test')
RETURNING id::text`, tenantID).Scan(&environmentID); err != nil {
		t.Fatalf("insert environment: %v", err)
	}

	var serviceID string
	if err := db.QueryRowContext(ctx, `
INSERT INTO services (tenant_id, environment_id, name, framework)
VALUES ($1, $2, 'checkout-api', 'goravel')
RETURNING id::text`, tenantID, environmentID).Scan(&serviceID); err != nil {
		t.Fatalf("insert service: %v", err)
	}

	return tenantID, environmentID, serviceID
}

func getRemediationPlan(t *testing.T, ctx context.Context, workflowStore store.WorkflowStore, tenantID string, id string) store.ContractRecord[contractsv1.RemediationPlan] {
	t.Helper()

	var record store.ContractRecord[contractsv1.RemediationPlan]
	if err := workflowStore.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.RemediationPlans().Get(ctx, tenantID, id)
		return err
	}); err != nil {
		t.Fatalf("get remediation plan %s: %v", id, err)
	}
	return record
}

func assertCount(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) {
	t.Helper()

	want := args[len(args)-1].(int)
	args = args[:len(args)-1]

	var got int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if got != want {
		t.Fatalf("unexpected count: got %d want %d", got, want)
	}
}

func withSearchPath(t *testing.T, dsn string, schemaName string) string {
	t.Helper()

	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		values := parsed.Query()
		values.Set("search_path", schemaName)
		parsed.RawQuery = values.Encode()
		return parsed.String()
	}
	if strings.TrimSpace(dsn) == "" {
		t.Fatal("empty PostgreSQL DSN")
	}
	return dsn + " search_path=" + schemaName
}

func quoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func randomHex(t *testing.T, bytesCount int) string {
	t.Helper()

	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generate random suffix: %v", err)
	}
	return hex.EncodeToString(raw)
}
