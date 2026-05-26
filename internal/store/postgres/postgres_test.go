package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store"
)

func TestUpdateStatusRejectsInvalidTransitionBeforeSQL(t *testing.T) {
	t.Parallel()

	q := &recordingQueryer{rowsAffected: 1}
	repo := investigationClusterRepo{q: q}
	err := repo.UpdateStatus(
		context.Background(),
		"tenant-1",
		"cluster-1",
		contractsv1.InvestigationStatusCompleted,
		contractsv1.InvestigationStatusRunning,
	)
	if err == nil {
		t.Fatal("expected invalid transition error")
	}
	if q.execCount != 0 {
		t.Fatalf("expected no SQL execution for invalid transition, got %d calls", q.execCount)
	}
}

func TestUpdateStatusUsesOptimisticStatusPredicate(t *testing.T) {
	t.Parallel()

	q := &recordingQueryer{rowsAffected: 1}
	repo := remediationPlanRepo{q: q}
	err := repo.UpdateStatus(
		context.Background(),
		"tenant-1",
		"plan-1",
		contractsv1.RemediationStatusPlanning,
		contractsv1.RemediationStatusAwaitingApproval,
	)
	if err != nil {
		t.Fatalf("expected status update to succeed: %v", err)
	}
	if !strings.Contains(q.query, "UPDATE remediation_plans") {
		t.Fatalf("expected remediation_plans update, got %q", q.query)
	}
	if !strings.Contains(q.query, "status = $3") {
		t.Fatalf("expected optimistic status predicate in query, got %q", q.query)
	}
	if got, want := q.args[2], string(contractsv1.RemediationStatusPlanning); got != want {
		t.Fatalf("unexpected from-status argument: got %v want %v", got, want)
	}
	if got, want := q.args[3], string(contractsv1.RemediationStatusAwaitingApproval); got != want {
		t.Fatalf("unexpected to-status argument: got %v want %v", got, want)
	}
}

func TestUpdateStatusReturnsConflictWhenNoRowsChange(t *testing.T) {
	t.Parallel()

	q := &recordingQueryer{rowsAffected: 0}
	repo := remediationAttemptRepo{q: q}
	err := repo.UpdateStatus(
		context.Background(),
		"tenant-1",
		"attempt-1",
		contractsv1.RemediationStatusRunning,
		contractsv1.RemediationStatusSucceeded,
	)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestPayloadJSONPrefersStoredSnapshot(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"id":"snapshot-id"}`)
	got, err := payloadJSON(store.ContractRecord[contractsv1.InvestigationRequest]{
		Payload: contractsv1.InvestigationRequest{
			ID: "payload-id",
		},
		PayloadJSON: raw,
	})
	if err != nil {
		t.Fatalf("expected payload json: %v", err)
	}
	if got != string(raw) {
		t.Fatalf("expected existing snapshot %s, got %s", raw, got)
	}
}

func TestCreateDiagnosisRequiresBranchRelationBeforeSQL(t *testing.T) {
	t.Parallel()

	q := &recordingQueryer{rowsAffected: 1}
	repo := diagnosisResultRepo{q: q}
	err := repo.Create(context.Background(), store.ContractRecord[contractsv1.DiagnosisResult]{
		TenantID:      "tenant-1",
		EnvironmentID: "env-1",
		ServiceID:     "service-1",
		Payload: contractsv1.DiagnosisResult{
			ID:              "diag-1",
			ContractVersion: contractsv1.ContractVersion,
			Status:          contractsv1.DiagnosisStatusComplete,
			Summary:         "summary",
		},
	})
	if err == nil {
		t.Fatal("expected missing branch relation error")
	}
	if q.execCount != 0 {
		t.Fatalf("expected no SQL execution when branch relation is missing, got %d calls", q.execCount)
	}
}

type recordingQueryer struct {
	query        string
	args         []any
	execCount    int
	rowsAffected int64
	err          error
}

func (q *recordingQueryer) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	q.execCount++
	q.query = query
	q.args = args
	if q.err != nil {
		return nil, q.err
	}
	return recordingResult(q.rowsAffected), nil
}

func (q *recordingQueryer) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unexpected QueryContext call")
}

func (q *recordingQueryer) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

type recordingResult int64

func (r recordingResult) LastInsertId() (int64, error) {
	return 0, errors.New("last insert id is not supported")
}

func (r recordingResult) RowsAffected() (int64, error) {
	return int64(r), nil
}
