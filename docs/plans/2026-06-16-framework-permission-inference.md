# Framework Permission Inference Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Let permission-drift readiness remediation infer framework permission targets when the policy does not explicitly list `permission_targets`.

**Architecture:** Reuse the existing `internal/runtime/permissions` framework policy engine as the source of framework defaults. Convert inferred framework expected paths into the readiness resolver's bounded permission targets, then preserve the existing Docker/local repair, verification, receipt, and rollback behavior.

**Tech Stack:** Go, existing readiness resolver, existing runtime permission policy engine, Docker readiness lab.

---

### Task 1: Expose Framework Permission Policies

**Files:**
- Modify: `internal/runtime/permissions/permissions.go`
- Test: `internal/runtime/permissions/permissions_test.go`

**Steps:**
1. Write a failing test that calls an exported framework-policy inference API for Laravel, Rails, Express, Flask/FastAPI, Go, Java, and Ruby.
2. Run the focused test and verify it fails because the API does not exist.
3. Export a minimal policy inference function without widening apply behavior.
4. Run focused tests and `go test ./internal/runtime/permissions -count=1`.

### Task 2: Use Inferred Policies In Readiness Resolver

**Files:**
- Modify: `internal/readinessresolve/readinessresolve.go`
- Test: `internal/readinessresolve/readinessresolve_test.go`

**Steps:**
1. Write a failing resolver test where permission-drift policy has no explicit targets, app files identify Laravel, and the resolver repairs a framework path.
2. Add conversion from inferred framework expected paths to readiness permission targets.
3. Keep explicit policy targets authoritative when present.
4. Run focused resolver tests and `go test ./internal/readinessresolve -count=1`.

### Task 3: Add Black-Box Inference Coverage

**Files:**
- Modify: `labs/readiness/bin/run-docker-lab.sh`
- Modify or add: `labs/readiness/policies/*`
- Test: black-box readiness run

**Steps:**
1. Add an inference-only permission-drift scenario or mode where the policy omits `permission_targets`.
2. Verify fixture health still passes.
3. Run a focused permission-drift benchmark proving AI LogFixer repairs from inferred framework targets.
4. Run full validation before publishing.
