-- PostgreSQL reference schema for the durable AI LogFixer workflow store.
--
-- Design posture:
-- - relational columns hold tenant, ownership, status, timestamps, and query paths
-- - tenant/environment/service IDs are database UUIDs
-- - contract record IDs are text because public v1 schemas expose string IDs
-- - payload_json stores the validated public v1 contract snapshot
-- - state transitions are enforced by the workflow/domain layer, with optimistic
--   status updates and audit rows written in the same transaction
-- - large logs, diffs, prompts, snapshots, and manifests live as artifacts

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tenants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    slug text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE environments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name text NOT NULL,
    kind text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE services (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name text NOT NULL,
    framework text,
    repository_url text,
    default_branch text,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, environment_id, name)
);

CREATE TABLE source_adapters (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    adapter_type text NOT NULL,
    display_name text NOT NULL,
    config_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE signal_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    source text NOT NULL,
    severity text,
    route text,
    method text,
    status_code integer,
    error_class text,
    fingerprint_hash text,
    idempotency_key text NOT NULL,
    observed_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    payload_json jsonb NOT NULL,
    UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE signal_fingerprints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    fingerprint_hash text NOT NULL,
    status text NOT NULL DEFAULT 'open',
    first_seen_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    occurrence_count bigint NOT NULL DEFAULT 1,
    sample_event_id uuid REFERENCES signal_events(id) ON DELETE SET NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, service_id, fingerprint_hash),
    CONSTRAINT signal_fingerprints_status_check CHECK (status IN ('open', 'linked', 'suppressed', 'resolved'))
);

CREATE TABLE investigation_requests (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    contract_version text NOT NULL DEFAULT 'v1',
    status text NOT NULL,
    source text NOT NULL,
    severity text,
    idempotency_key text NOT NULL,
    requested_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    lock_version bigint NOT NULL DEFAULT 0,
    payload_json jsonb NOT NULL,
    UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT investigation_requests_status_check CHECK (status IN ('requested', 'fingerprinted', 'linked', 'queued', 'running', 'needs_more_data', 'completed', 'failed', 'rejected'))
);

CREATE TABLE investigation_clusters (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    fingerprint_id uuid REFERENCES signal_fingerprints(id) ON DELETE SET NULL,
    contract_version text NOT NULL DEFAULT 'v1',
    status text NOT NULL,
    summary text NOT NULL,
    priority text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    lock_version bigint NOT NULL DEFAULT 0,
    payload_json jsonb NOT NULL,
    CONSTRAINT investigation_clusters_status_check CHECK (status IN ('requested', 'fingerprinted', 'linked', 'queued', 'running', 'needs_more_data', 'completed', 'failed', 'rejected'))
);

CREATE TABLE investigation_branches (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    cluster_id text NOT NULL REFERENCES investigation_clusters(id) ON DELETE CASCADE,
    request_id text REFERENCES investigation_requests(id) ON DELETE SET NULL,
    contract_version text NOT NULL DEFAULT 'v1',
    status text NOT NULL,
    hypothesis text,
    assigned_worker_id text,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    lock_version bigint NOT NULL DEFAULT 0,
    payload_json jsonb NOT NULL,
    CONSTRAINT investigation_branches_status_check CHECK (status IN ('requested', 'fingerprinted', 'linked', 'queued', 'running', 'needs_more_data', 'completed', 'failed', 'rejected'))
);

CREATE TABLE investigation_decisions (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    branch_id text NOT NULL REFERENCES investigation_branches(id) ON DELETE CASCADE,
    contract_version text NOT NULL DEFAULT 'v1',
    decision text NOT NULL,
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload_json jsonb NOT NULL
);

CREATE TABLE evidence_items (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    branch_id text REFERENCES investigation_branches(id) ON DELETE SET NULL,
    contract_version text NOT NULL DEFAULT 'v1',
    evidence_type text NOT NULL,
    source text NOT NULL,
    collected_at timestamptz NOT NULL,
    redaction_state text NOT NULL DEFAULT 'redacted',
    artifact_id text,
    payload_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT evidence_items_redaction_state_check CHECK (redaction_state IN ('raw', 'redacted', 'not_needed', 'unknown', 'failed', 'blocked'))
);

CREATE TABLE diagnosis_results (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    branch_id text NOT NULL REFERENCES investigation_branches(id) ON DELETE CASCADE,
    contract_version text NOT NULL DEFAULT 'v1',
    status text NOT NULL,
    safety_classification text,
    confidence numeric(5, 4),
    summary text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    lock_version bigint NOT NULL DEFAULT 0,
    payload_json jsonb NOT NULL,
    CONSTRAINT diagnosis_results_status_check CHECK (status IN ('pending', 'complete', 'failed', 'needs_more_data', 'unsupported_source', 'blocked_by_safety'))
);

CREATE TABLE runbook_recommendations (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    diagnosis_result_id text NOT NULL REFERENCES diagnosis_results(id) ON DELETE CASCADE,
    contract_version text NOT NULL DEFAULT 'v1',
    title text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload_json jsonb NOT NULL
);

CREATE TABLE rollback_plans (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    contract_version text NOT NULL DEFAULT 'v1',
    strategy text NOT NULL,
    availability text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload_json jsonb NOT NULL
);

CREATE TABLE patch_plans (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    diagnosis_result_id text REFERENCES diagnosis_results(id) ON DELETE CASCADE,
    rollback_plan_id text REFERENCES rollback_plans(id) ON DELETE SET NULL,
    contract_version text NOT NULL DEFAULT 'v1',
    risk_level text NOT NULL,
    approval_required boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload_json jsonb NOT NULL
);

CREATE TABLE remediation_plans (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    diagnosis_result_id text NOT NULL REFERENCES diagnosis_results(id) ON DELETE CASCADE,
    rollback_plan_id text REFERENCES rollback_plans(id) ON DELETE SET NULL,
    contract_version text NOT NULL DEFAULT 'v1',
    status text NOT NULL,
    risk_level text NOT NULL,
    approval_required boolean NOT NULL,
    summary text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    lock_version bigint NOT NULL DEFAULT 0,
    payload_json jsonb NOT NULL,
    CONSTRAINT remediation_plans_status_check CHECK (status IN ('pending', 'planning', 'awaiting_approval', 'approved', 'denied', 'running', 'monitoring', 'succeeded', 'failed', 'rolled_back', 'escalated')),
    CONSTRAINT remediation_plans_risk_level_check CHECK (risk_level IN ('read_only', 'low_risk', 'medium_risk', 'high_risk', 'critical_risk', 'blocked'))
);

CREATE TABLE approval_requests (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    remediation_plan_id text NOT NULL REFERENCES remediation_plans(id) ON DELETE CASCADE,
    contract_version text NOT NULL DEFAULT 'v1',
    status text NOT NULL,
    requested_by text,
    decided_by text,
    requested_at timestamptz NOT NULL,
    expires_at timestamptz,
    decided_at timestamptz,
    decision_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    lock_version bigint NOT NULL DEFAULT 0,
    payload_json jsonb NOT NULL,
    CONSTRAINT approval_requests_status_check CHECK (status IN ('pending', 'approved', 'denied', 'expired'))
);

CREATE TABLE approval_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    approval_request_id text NOT NULL REFERENCES approval_requests(id) ON DELETE CASCADE,
    actor_id text NOT NULL,
    decision text NOT NULL,
    reason text,
    decided_at timestamptz NOT NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT approval_decisions_decision_check CHECK (decision IN ('approved', 'denied', 'expired'))
);

CREATE TABLE remediation_attempts (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    remediation_plan_id text NOT NULL REFERENCES remediation_plans(id) ON DELETE CASCADE,
    approval_request_id text REFERENCES approval_requests(id) ON DELETE SET NULL,
    contract_version text NOT NULL DEFAULT 'v1',
    status text NOT NULL,
    executor_type text NOT NULL,
    idempotency_key text NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    rollback_attempt_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    lock_version bigint NOT NULL DEFAULT 0,
    payload_json jsonb NOT NULL,
    UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT remediation_attempts_status_check CHECK (status IN ('pending', 'planning', 'awaiting_approval', 'approved', 'denied', 'running', 'monitoring', 'succeeded', 'failed', 'rolled_back', 'escalated'))
);

CREATE TABLE remediation_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    remediation_attempt_id text NOT NULL REFERENCES remediation_attempts(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    message text NOT NULL,
    severity text NOT NULL DEFAULT 'info',
    occurred_at timestamptz NOT NULL,
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE rollback_attempts (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    remediation_attempt_id text NOT NULL REFERENCES remediation_attempts(id) ON DELETE CASCADE,
    status text NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    manifest_artifact_id text,
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rollback_attempts_status_check CHECK (status IN ('pending', 'running', 'succeeded', 'failed'))
);

ALTER TABLE remediation_attempts
    ADD CONSTRAINT remediation_attempts_rollback_attempt_fk
    FOREIGN KEY (rollback_attempt_id) REFERENCES rollback_attempts(id) ON DELETE SET NULL;

CREATE TABLE receipts (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    remediation_attempt_id text NOT NULL REFERENCES remediation_attempts(id) ON DELETE CASCADE,
    contract_version text NOT NULL DEFAULT 'v1',
    outcome text NOT NULL,
    issued_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload_json jsonb NOT NULL
);

CREATE TABLE agent_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    remediation_plan_id text REFERENCES remediation_plans(id) ON DELETE SET NULL,
    remediation_attempt_id text REFERENCES remediation_attempts(id) ON DELETE SET NULL,
    agent_name text NOT NULL,
    model text,
    status text NOT NULL,
    prompt_artifact_id text,
    diff_artifact_id text,
    validation_artifact_id text,
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_runs_status_check CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'blocked'))
);

CREATE TABLE artifacts (
    id text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    service_id uuid REFERENCES services(id) ON DELETE SET NULL,
    artifact_type text NOT NULL,
    content_hash text NOT NULL,
    byte_size bigint,
    media_type text,
    storage_uri text NOT NULL,
    redaction_state text NOT NULL DEFAULT 'redacted',
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (tenant_id, content_hash),
    CONSTRAINT artifacts_redaction_state_check CHECK (redaction_state IN ('raw', 'redacted', 'not_needed', 'unknown', 'failed', 'blocked'))
);

ALTER TABLE evidence_items
    ADD CONSTRAINT evidence_items_artifact_fk
    FOREIGN KEY (artifact_id) REFERENCES artifacts(id) ON DELETE SET NULL;

ALTER TABLE rollback_attempts
    ADD CONSTRAINT rollback_attempts_manifest_artifact_fk
    FOREIGN KEY (manifest_artifact_id) REFERENCES artifacts(id) ON DELETE SET NULL;

ALTER TABLE agent_runs
    ADD CONSTRAINT agent_runs_prompt_artifact_fk
    FOREIGN KEY (prompt_artifact_id) REFERENCES artifacts(id) ON DELETE SET NULL,
    ADD CONSTRAINT agent_runs_diff_artifact_fk
    FOREIGN KEY (diff_artifact_id) REFERENCES artifacts(id) ON DELETE SET NULL,
    ADD CONSTRAINT agent_runs_validation_artifact_fk
    FOREIGN KEY (validation_artifact_id) REFERENCES artifacts(id) ON DELETE SET NULL;

CREATE TABLE audit_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    actor_id text,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    event_type text NOT NULL,
    message text NOT NULL,
    before_state text,
    after_state text,
    correlation_id text,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workflow_leases (
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    owner_id text NOT NULL,
    fencing_token bigint NOT NULL,
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT now(),
    renewed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_type, resource_id)
);

CREATE TABLE outbox_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    claimed_by text,
    claimed_at timestamptz,
    payload_json jsonb NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    last_error_message text,
    UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT outbox_events_status_check CHECK (status IN ('pending', 'claimed', 'published', 'failed', 'dead'))
);

CREATE TABLE external_refs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    external_system text NOT NULL,
    external_type text NOT NULL,
    external_id text NOT NULL,
    url text,
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE knowledge_refs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    graph_id text NOT NULL,
    node_id text NOT NULL,
    relation_type text,
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX signal_events_tenant_service_observed_at_idx ON signal_events (tenant_id, service_id, observed_at DESC);
CREATE INDEX signal_events_tenant_fingerprint_idx ON signal_events (tenant_id, fingerprint_hash) WHERE fingerprint_hash IS NOT NULL;
CREATE INDEX signal_fingerprints_tenant_hash_status_idx ON signal_fingerprints (tenant_id, fingerprint_hash, status);

CREATE INDEX investigation_requests_tenant_service_created_at_idx ON investigation_requests (tenant_id, service_id, created_at DESC);
CREATE INDEX investigation_clusters_tenant_status_updated_at_idx ON investigation_clusters (tenant_id, status, updated_at DESC);
CREATE INDEX investigation_branches_tenant_cluster_status_idx ON investigation_branches (tenant_id, cluster_id, status);

CREATE INDEX evidence_items_tenant_branch_created_at_idx ON evidence_items (tenant_id, branch_id, created_at DESC);
CREATE INDEX diagnosis_results_tenant_branch_status_idx ON diagnosis_results (tenant_id, branch_id, status);
CREATE INDEX remediation_plans_tenant_diagnosis_status_idx ON remediation_plans (tenant_id, diagnosis_result_id, status);
CREATE INDEX approval_requests_tenant_status_expires_at_idx ON approval_requests (tenant_id, status, expires_at);
CREATE INDEX remediation_attempts_tenant_plan_status_idx ON remediation_attempts (tenant_id, remediation_plan_id, status);
CREATE INDEX remediation_events_tenant_attempt_occurred_at_idx ON remediation_events (tenant_id, remediation_attempt_id, occurred_at DESC);
CREATE INDEX receipts_tenant_attempt_idx ON receipts (tenant_id, remediation_attempt_id);

CREATE INDEX artifacts_tenant_type_created_at_idx ON artifacts (tenant_id, artifact_type, created_at DESC);
CREATE INDEX audit_events_tenant_resource_created_at_idx ON audit_events (tenant_id, resource_type, resource_id, created_at DESC);
CREATE INDEX audit_events_tenant_correlation_idx ON audit_events (tenant_id, correlation_id) WHERE correlation_id IS NOT NULL;
CREATE INDEX workflow_leases_expires_at_idx ON workflow_leases (expires_at);
CREATE INDEX outbox_events_due_idx ON outbox_events (tenant_id, status, next_attempt_at) WHERE status IN ('pending', 'failed');
CREATE INDEX external_refs_tenant_resource_idx ON external_refs (tenant_id, resource_type, resource_id);
CREATE INDEX knowledge_refs_tenant_resource_idx ON knowledge_refs (tenant_id, resource_type, resource_id);
