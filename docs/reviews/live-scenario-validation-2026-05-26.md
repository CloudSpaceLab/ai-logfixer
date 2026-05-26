# Live Scenario Validation

Review date: 2026-05-26

This note records the live scenario checks run from this branch and the public real-world log/error corpora reviewed as future fixture sources.

## Local Live Runs

### Runtime V2 Demo App

Scenario:

- built and started `cmd/demo-goravel-app` as a real local HTTP process on `127.0.0.1:18090`
- initialized the app with a bad upstream URL
- generated five real `/orders` requests returning `503`
- ran `cmd/ai-logfixer-v2`
- verified `/orders` returned `200` in the same running app process

Result:

```text
before: 503,503,503,503,503
after:  200
remediation_plan.status: succeeded
attempt.status:          succeeded
receipt.outcome:         succeeded
```

The run output was written under ignored `the ignored live scenario temp directory`.

### Real Goravel App

Scenario:

- reused the ignored real Goravel clone in `tmp/real-goravel-app`
- injected `panic("live scenario fault: user controller crashed before response")` into `UserController.Index`
- built and started the real Goravel app on `127.0.0.1:3000`
- generated five real `/users` requests returning `500`
- ran `cmd/ai-logfixer-goravel` in dry-run mode
- ran `cmd/ai-logfixer-goravel` in approved apply mode with restart and verify commands
- verified `/users` returned `200` after patch/restart

Result:

```text
before: 500,500,500,500,500
dry-run route:          /users
dry-run handler:        UserController.Index
dry-run plan status:    awaiting_approval
apply attempt status:   succeeded
apply receipt outcome:  succeeded
source patched:         true
service restarted:      true
route verified:         true
after:                  200
```

The clean JSON outputs were written under:

```text
tmp/real-goravel-app/ai-logfixer-live3-dry-run.json
tmp/real-goravel-app/ai-logfixer-live3-apply.json
```

## Public Corpus Review

### Loghub

URL: https://github.com/logpai/loghub

Usefulness:

- best immediate base for real-world log fixtures
- includes Apache error logs, OpenStack infrastructure logs, Hadoop, Spark, ZooKeeper, Linux, OpenSSH, and others
- Zenodo mirror provides small archives such as Apache and OpenStack that are practical for CI-sized fixture generation

Local inspection:

```text
Apache.log lines:               56,482
Apache error/warn-like lines:   38,249
openstack_abnormal.log lines:   18,434
OpenStack error-like lines:     197
```

Downloaded only to ignored `tmp/corpora/loghub/`.

Fit for AI LogFixer:

- Apache corpus is strong for web-server error grouping and repeated infrastructure misconfiguration signals.
- OpenStack corpus is strong for request-id correlation and exception/event grouping.
- Neither maps to a source patch automatically without a new adapter because they do not include local application route maps or source ownership.

### AERI Stacktraces

URL: https://download.eclipse.org/scava/aeri_stacktraces/

Usefulness:

- best public stacktrace corpus found for crash/error deduplication
- includes Eclipse IDE exception incidents and grouped problems
- useful for future diagnosis clustering, stacktrace fingerprinting, and root-cause grouping tests

Fit for AI LogFixer:

- excellent for investigation clustering and dedupe logic
- not a remediation fixture by itself because it does not provide a runnable target app
- large raw data means we should start from the smaller extracts

### AIT AECID Anomaly Detection Log Datasets

URL: https://github.com/ait-aecid/anomaly-detection-log-datasets

Usefulness:

- provides analysis scripts and preprocessed samples for common anomaly-detection log datasets
- useful as a baseline for sequence grouping and anomaly classification tests

Fit for AI LogFixer:

- good benchmark harness candidate
- less direct than Loghub for our product path because it focuses on anomaly-detection evaluation rather than remediation contracts

## Biggest Testing Gaps Exposed

1. The current product path is strong for framework-aware Goravel route failure remediation and Runtime V2 config remediation.
2. We do not yet have a generic log-ingestion adapter that can consume Apache/OpenStack/Hadoop-style corpora.
3. We need a fixture generator that converts corpus excerpts into contract-valid `InvestigationRequest`, `DiagnosisResult`, and non-applicable/blocked `RemediationPlan` records.
4. We need negative tests proving the system escalates safely when logs show a real incident but no source ownership or safe patch path exists.

## Recommended Next Test Slice

Add a `internal/signals/loghub` package or equivalent fixture helper that can:

```text
1. Load a small checked-in excerpt derived from Loghub Apache/OpenStack formats.
2. Group repeated error signatures.
3. Build an investigation request and evidence item.
4. Emit a blocked/escalated remediation plan when no framework adapter/source owner exists.
```

That gives us realistic corpus-backed detection coverage without pretending Apache/OpenStack logs can be automatically source-patched by the current Goravel adapter.
