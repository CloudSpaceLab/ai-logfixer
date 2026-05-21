# Standalone ai-logfixer C4 Design With Remediation

## Product Positioning

`ai-logfixer` is a standalone investigation and remediation product.

It can be triggered automatically by observed signals such as repeated 503 errors in logs, or manually by external platforms such as ControlOne. ControlOne is an integration consumer, not an internal dependency.

```text
+---------------------+                      +----------------------+
| Go App On Server    |                      | ControlOne User      |
|---------------------|                      |----------------------|
| emits logs/errors   |                      | sees abnormality     |
| like repeated 503s  |                      | starts investigation |
+----------+----------+                      +----------+-----------+
           |                                            |
           | logs / events                              | API request
           v                                            v
+---------------------------------------------------------------+
|                         ai-logfixer                           |
|---------------------------------------------------------------|
| Detects incidents, starts investigations, correlates related   |
| work, limits overload, recommends fixes, executes approved     |
| remediation, rolls back when needed, and records receipts.     |
+------------------+----------------------+---------------------+
                   |                      |
                   v                      v
        +-------------------+     +----------------------+
        | Observability     |     | External Platforms   |
        | Sources           |     |----------------------|
        | logs, traces,     |     | ControlOne, Slack,   |
        | metrics, DB facts |     | Jira, CI/CD          |
        +-------------------+     +----------------------+
```

## C4 Level 1: System Context

```text
+-------------------+       logs/events       +-------------------+
| Runtime Systems   | ----------------------> |                   |
|-------------------|                         |                   |
| apps, web servers,|                         |                   |
| DBMS, cloud infra |                         |                   |
+-------------------+                         |                   |
                                             |   ai-logfixer     |
+-------------------+       API/webhooks      |                   |
| External Users    | ----------------------> |                   |
| and Platforms     |                         |                   |
|-------------------| <---------------------- |                   |
| ControlOne, CLI,  |   status/results        +---------+---------+
| ticketing, chat   |                                   |
+-------------------+                                   |
                                                        | guarded actions
                                                        v
                                             +-------------------+
                                             | Target Systems    |
                                             |-------------------|
                                             | configs, deploys, |
                                             | DBs, dependencies |
                                             +-------------------+
```

## C4 Level 2: Containers

```text
+--------------------------------------------------------------------+
|                           ai-logfixer                              |
|--------------------------------------------------------------------|
|                                                                    |
| +----------------+     +----------------+     +------------------+ |
| | Web UI         |     | Public API     |     | CLI              | |
| |----------------|     |----------------|     |------------------| |
| | native product |     | integrations   |     | local/server ops | |
| | dashboard      |     | ControlOne     |     | workflows        | |
| +-------+--------+     +-------+--------+     +--------+---------+ |
|         |                      |                       |           |
|         +----------------------+-----------------------+           |
|                                |                                   |
|                                v                                   |
| +----------------------------------------------------------------+ |
| | Investigation Orchestrator                                     | |
| |----------------------------------------------------------------| |
| | start, attach, link, run in parallel, queue, reject, explain   | |
| +----------+----------------------+--------------------+----------+ |
|            |                      |                    |            |
|            v                      v                    v            |
| +-------------------+  +-------------------+  +------------------+ |
| | Signal Detectors  |  | Correlation Engine|  | Capacity Manager | |
| |-------------------|  |-------------------|  |------------------| |
| | watches logs,     |  | finds related     |  | limits active    | |
| | metrics, traces   |  | investigations    |  | work             | |
| +---------+---------+  +---------+---------+  +---------+--------+ |
|           |                      |                      |          |
|           v                      v                      v          |
| +----------------------------------------------------------------+ |
| | Investigation Workers                                           | |
| |----------------------------------------------------------------| |
| | evidence collection, diagnosis, recommendations, patch preview  | |
| +----------------------+----------------------+------------------+ |
|                        |                      |                    |
|                        v                      v                    |
| +----------------------------------------------------------------+ |
| | Remediation Runtime                                             | |
| |----------------------------------------------------------------| |
| | plan, approve, execute, monitor, rollback, verify, receipt      | |
| +----------------------+----------------------+------------------+ |
|                        |                      |                    |
|                        v                      v                    |
|           +---------------------+    +--------------------------+  |
|           | Evidence Store      |    | Investigation Store      |  |
|           |---------------------|    |--------------------------|  |
|           | normalized evidence |    | state, queue, links,     |  |
|           | and summaries       |    | receipts, decisions      |  |
|           +---------------------+    +--------------------------+  |
|                                                                    |
+--------------------------------------------------------------------+
```

## C4 Level 3: Investigation Orchestrator

```text
+--------------------------------------------------------------------+
|                    Investigation Orchestrator                       |
|--------------------------------------------------------------------|
|                                                                    |
| +---------------------+                                            |
| | Investigation Intake|                                            |
| |---------------------|                                            |
| | receives automatic  |                                            |
| | and manual requests |                                            |
| +----------+----------+                                            |
|            |                                                       |
|            v                                                       |
| +---------------------+       +----------------------+             |
| | Signal Fingerprinter| ----> | Correlation Engine   |             |
| |---------------------|       |----------------------|             |
| | service, error,     |       | duplicate, related,  |             |
| | time, source, deploy|       | unrelated            |             |
| +---------------------+       +----------+-----------+             |
|                                          |                         |
|                                          v                         |
|                            +----------------------------+          |
|                            | Investigation Router       |          |
|                            |----------------------------|          |
|                            | start / attach / link /    |          |
|                            | queue / reject             |          |
|                            +-------------+--------------+          |
|                                          |                         |
|                                          v                         |
| +---------------------+       +----------------------+             |
| | Capacity Manager    | <---- | Investigation State  |             |
| |---------------------|       | Store                |             |
| | max active jobs,    |       |----------------------|             |
| | queue limits,       |       | active, queued,      |             |
| | cooldowns           |       | completed, linked    |             |
| +----------+----------+       +----------+-----------+             |
|            |                             |                         |
|            v                             v                         |
| +---------------------+       +----------------------+             |
| | Explanation Builder |       | Worker Scheduler     |             |
| |---------------------|       |----------------------|             |
| | explains merged,    |       | starts investigation |             |
| | linked, queued, or  |       | workers when allowed |             |
| | rejected requests   |       +----------+-----------+             |
| +---------------------+                  |                         |
|                                          v                         |
|                              +-----------------------+             |
|                              | Investigation Worker  |             |
|                              +-----------------------+             |
|                                                                    |
+--------------------------------------------------------------------+
```

## C4 Level 3: Investigation Worker

```text
+----------------------------------------------------------------+
|                    Investigation Worker                        |
|----------------------------------------------------------------|
|                                                                |
| +-------------------+      +--------------------+              |
| | Scope Builder     | ---> | Evidence Collector |              |
| |-------------------|      |--------------------|              |
| | service, time,    |      | logs, traces,      |              |
| | symptom, sources  |      | metrics, DB facts  |              |
| +---------+---------+      +---------+----------+              |
|           |                          |                         |
|           v                          v                         |
| +-------------------+      +--------------------+              |
| | Evidence Normalizer| --->| Diagnosis Engine   |              |
| |-------------------|      |--------------------|              |
| | standard evidence  |     | likely cause,      |              |
| | shape              |     | confidence         |              |
| +---------+----------+     +---------+----------+              |
|           |                          |                         |
|           v                          v                         |
| +-------------------+      +--------------------+              |
| | Recommendation    | ---> | Remediation Request|              |
| | Engine            |      | Builder            |              |
| |-------------------|      |--------------------|              |
| | next action       |      | candidate fix,     |              |
| | candidates        |      | target, constraints|              |
| +---------+---------+      +---------+----------+              |
|           |                          |                         |
|           +-------------+------------+                         |
|                         v                                      |
|              +--------------------+                            |
|              | Result Publisher   |                            |
|              |--------------------|                            |
|              | updates UI/API and |                            |
|              | notifies runtime   |                            |
|              +--------------------+                            |
|                                                                |
+----------------------------------------------------------------+
```

## C4 Level 3: Remediation Runtime

Remediation is the controlled journey from "we think we know the fix" to "we safely changed the system and proved whether it worked."

```text
+-----------------------------+
| 1. Candidate Fix Found      |
|-----------------------------|
| ai-logfixer thinks it knows |
| what change may fix the     |
| problem.                    |
+-------------+---------------+
              |
              v
+-----------------------------+
| 2. Build Fix Preview        |
|-----------------------------|
| Show exactly what would     |
| change before touching      |
| anything.                   |
+-------------+---------------+
              |
              v
+-----------------------------+
| 3. Build Rollback Plan      |
|-----------------------------|
| Decide how to undo the      |
| change if it fails.         |
+-------------+---------------+
              |
              v
+-----------------------------+
| 4. Safety Check             |
|-----------------------------|
| Decide if this is low risk, |
| high risk, or blocked.      |
+-------------+---------------+
              |
              v
+-----------------------------+
| 5. Need Approval?           |
+-------------+---------------+
              |
        +-----+------+
        |            |
       No           Yes
        |            |
        v            v
+-------------+   +-----------------------+
| 6A. Auto    |   | 6B. Ask User          |
| Apply If    |   |-----------------------|
| Safe        |   | Explain the fix, risk,|
+------+------+   | rollback, and wait   |
       |          | for approval.         |
       |          +----------+------------+
       |                     |
       |              +------+------+
       |              |             |
       |          Approved       Denied
       |              |             |
       |              v             v
       |       +-------------+   +----------------+
       |       | Apply Fix   |   | Stop And Save  |
       |       | Carefully   |   | Decision       |
       |       +------+------+   +----------------+
       |              |
       +--------------+
              |
              v
+-----------------------------+
| 7. Watch The System         |
|-----------------------------|
| Did errors stop? Did health |
| improve? Did new errors     |
| appear?                     |
+-------------+---------------+
              |
       +------+------+
       |             |
    Worked        Failed
       |             |
       v             v
+-------------+   +-----------------------+
| 8A. Save    |   | 8B. Rollback Or       |
| Success     |   | Escalate              |
| Receipt     |   |-----------------------|
+-------------+   | Undo if possible, or  |
                  | ask human for help.   |
                  +----------+------------+
                             |
                             v
                  +-----------------------+
                  | 9. Save Failure       |
                  | Receipt               |
                  +-----------------------+
```

## Remediation Decision Flow

```text
Problem understood
        |
        v
Candidate fix selected
        |
        v
Fix preview created
        |
        v
Rollback plan created
        |
        v
Risk checked
        |
        v
+--------------------+
| Approval required? |
+---------+----------+
          |
   +------+------+
   |             |
  No            Yes
   |             |
   v             v
Auto-apply   Ask user/platform
if safe      for approval
   |             |
   |      +------+------+
   |      |             |
   |   Approved       Denied
   |      |             |
   |      v             v
   |   Apply fix     Stop
   |      |
   +------+
          |
          v
Monitor result
          |
   +------+------+
   |             |
 Worked        Failed
   |             |
   v             v
Save        Rollback if possible
success          |
receipt          v
            Save failure
            receipt
```

The user should see progress throughout remediation:

```text
I found a likely fix.
        |
        v
This is what I want to change.
        |
        v
This is the rollback plan.
        |
        v
This is the risk level. I need approval.
        |
        v
Applying fix now.
        |
        v
Watching system health.
        |
        v
Fix worked. Receipt saved.
```

## Investigation Cluster Model

```text
--------------------------------------------------+
| Investigation Cluster: go-api abnormal errors    |
| after deploy v42                                 |
|--------------------------------------------------|
|                                                  |
| +----------------------------------------------+ |
| | Branch A: 503 availability errors            | |
| |----------------------------------------------| |
| | source: automatic logs                       | |
| | status: running                              | |
| | remediation: checking dependency timeouts    | |
| +----------------------------------------------+ |
|                                                  |
| +----------------------------------------------+ |
| | Branch B: 403 forbidden errors               | |
| |----------------------------------------------| |
| | source: ControlOne manual request            | |
| | status: linked / queued / running            | |
| | remediation: checking auth and WAF policy    | |
| +----------------------------------------------+ |
|                                                  |
| +----------------------------------------------+ |
| | Branch C: latency spike                      | |
| |----------------------------------------------| |
| | source: metrics detector                     | |
| | status: attached                             | |
| | remediation: no separate executor yet        | |
| +----------------------------------------------+ |
|                                                  |
+--------------------------------------------------+
```

## User Communication During Remediation

`ai-logfixer` should explain every major decision:

```text
- why an investigation started
- whether it is duplicate, related, or separate
- why a fix is recommended
- what the fix would change
- what rollback is available
- whether approval is required
- what is happening during execution
- whether the fix worked
- whether rollback was triggered
```

Example response when a 403 request arrives during an active 503 investigation:

```text
We are already investigating go-api availability errors from the same time window.

Your 403 request is related because it shares service, deploy version, and time window.
It is not identical because 403 usually points to auth or policy, while 503 usually
points to availability or dependency failure.

Decision:
- link this request to the active go-api incident cluster
- start a focused auth/policy branch if capacity is available
- otherwise attach the request and queue the branch
```

Example remediation update:

```text
I found a likely config mismatch in the upstream route policy.

Proposed fix:
- restore the previous route policy for /api/orders
- expected impact: low risk
- rollback: restore current policy snapshot

I need approval before applying this fix because it changes production routing.
```

## Capacity Rules

```text
max_global_active_investigations = 5
max_per_service_active_investigations = 2
max_related_branches_per_cluster = 3
max_active_remediations = 2
max_per_service_active_remediations = 1
queue_limit = 20
cooldown_window = 10 minutes
```

Remediation capacity is separate from investigation capacity because applying fixes is more dangerous than analyzing evidence.

## Core Rule

```text
Investigation can be parallel.
Remediation must be guarded.
Execution must be explainable, reversible when possible, and recorded.
```
