# RemediationAttempt

`RemediationAttempt` records an actual execution attempt for a remediation plan.

It tracks execution status, approval linkage, start/finish times, monitoring summary, rollback linkage, timeline events, and external references.

Boundary rule:

Running, monitoring, succeeded, failed, rolled back, and escalated attempts require an approval reference when approval is required.
