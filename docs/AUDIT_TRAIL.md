# Audit Trail Architecture

> **Audience**: Compliance officers, SOC 2 auditors, ISO 27001 assessors, and
> security reviewers. This document explains how Nightgauge records,
> protects, retains, and makes available a complete audit trail of all
> security-relevant activities.

---

## Table of Contents

1. [Overview](#overview)
2. [End-to-End Data Flow](#end-to-end-data-flow)
3. [Event Schema Reference](#event-schema-reference)
4. [Security Architecture](#security-architecture)
5. [Retention Policy](#retention-policy)
6. [Cross-Repository Event Correlation](#cross-repository-event-correlation)
7. [Compliance Mapping](#compliance-mapping)
8. [Operational Procedures](#operational-procedures)

---

## Overview

Nightgauge maintains a tamper-resistant audit trail for every security-
relevant action across the platform and its developer tooling. Events are
captured client-side by the SDK, transmitted over TLS to the Nightgauge
platform API, and stored in an append-only audit log in the platform database.

The audit trail covers:

- **Authentication and access** — logins, logouts, failed attempts
- **License and billing** — subscription changes, license creation and
  revocation
- **Team administration** — user invitations, role changes, removals
- **API key and webhook management** — creation and revocation of credentials
- **Pipeline activity** — every pipeline stage start, completion, and failure
- **Compliance operations** — report generation and data purges

Every event record is immutable once written. The platform does not support
in-place updates to audit entries.

---

## End-to-End Data Flow

The diagram below shows how an event travels from source to storage to
reporting.

```
┌─────────────────────────────────────────────────────────────────────────┐
│  Client-side (developer machine / CI runner)                            │
│                                                                         │
│  ┌────────────────────┐    enqueue()    ┌──────────────────────────┐   │
│  │  Pipeline Stage    │───────────────▶│   AuditEventClient       │   │
│  │  (HeadlessOrch.)   │                │   (nightgauge-sdk)   │   │
│  └────────────────────┘                │                          │   │
│                                        │  • Validates with Zod    │   │
│  ┌────────────────────┐                │  • Batches in memory     │   │
│  │  Platform UI       │                │  • Flushes on stage end  │   │
│  │  (VSCode ext.)     │                │    and pipeline complete  │   │
│  └────────────────────┘                └───────────┬──────────────┘   │
│                                                    │                   │
│                                  ┌─────────────────▼──────────────┐   │
│                                  │  Offline Queue (JSON file)      │   │
│                                  │  .nightgauge/              │   │
│                                  │    audit-queue.json             │   │
│                                  │                                 │   │
│                                  │  Used when network unavailable  │   │
│                                  │  Retried on next pipeline run   │   │
│                                  └─────────────────┬──────────────┘   │
└────────────────────────────────────────────────────┼────────────────────┘
                                                     │ HTTPS (TLS 1.2+)
                                                     │ POST /api/v1/audit/events
                                                     │ Authorization: Bearer <api-key>
                                     ┌───────────────▼────────────────────┐
                                     │  Platform API                       │
                                     │  (acme-platform)         │
                                     │                                     │
                                     │  • Authenticates API key            │
                                     │  • Validates event schema           │
                                     │  • Appends to audit_logs table      │
                                     │  • Assigns server-side timestamp    │
                                     └───────────────┬────────────────────┘
                                                     │
                                     ┌───────────────▼────────────────────┐
                                     │  Platform Database (PostgreSQL)     │
                                     │                                     │
                                     │  • Append-only audit_logs table     │
                                     │  • Indexed by userId, action,       │
                                     │    timestamp, resourceId            │
                                     └───────────────┬────────────────────┘
                                                     │
                              ┌──────────────────────▼─────────────────────┐
                              │  Reporting / Review Interfaces              │
                              │                                             │
                              │  • VSCode Dashboard — Audit Log Viewer tab  │
                              │  • REST API — GET /api/v1/audit/events      │
                              │  • CSV Export — filterable time windows     │
                              │  • Compliance Reports — scheduled or ad-hoc │
                              └─────────────────────────────────────────────┘
```

### Component Descriptions

| Component                  | Role                                                                                                                                                 |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AuditEventClient** (SDK) | Validates, batches, and ships events from developer machines. Written in TypeScript. Source: `packages/nightgauge-sdk/src/audit/AuditEventClient.ts` |
| **Offline Queue**          | JSON file on disk that buffers events when the platform is unreachable. Retried on the next pipeline completion.                                     |
| **Platform API**           | Authenticated HTTPS endpoint that receives event batches and writes them to storage.                                                                 |
| **Platform Database**      | PostgreSQL append-only table that stores the authoritative audit log.                                                                                |
| **Audit Log Viewer**       | VSCode Dashboard tab that queries the platform API and presents events with filtering and CSV export.                                                |

---

## Event Schema Reference

Every audit event stored in the platform contains the following fields.

### Core Event Fields

| Field          | Type                                | Required | Description                                                                       |
| -------------- | ----------------------------------- | -------- | --------------------------------------------------------------------------------- |
| `id`           | string (UUID)                       | Yes      | Platform-assigned unique identifier for this event                                |
| `timestamp`    | string (ISO 8601)                   | Yes      | Server-assigned UTC timestamp when the event was received                         |
| `userId`       | string                              | Yes      | Identifier of the user or service account that performed the action               |
| `userEmail`    | string                              | No       | Email address of the user, when available                                         |
| `action`       | string (enum)                       | Yes      | Machine-readable action identifier. See [Action Types](#action-types) below       |
| `resourceType` | string (≤ 100 chars)                | No       | Category of the resource affected (e.g., `issue`, `stage`, `license`)             |
| `resourceId`   | string (≤ 255 chars)                | No       | Specific resource identifier (e.g., issue number, pipeline run ID)                |
| `status`       | `success` \| `failure` \| `pending` | Yes      | Outcome of the action                                                             |
| `metadata`     | object                              | No       | Action-specific supplemental data (key/value pairs). Schema varies by action type |
| `costUsd`      | number                              | No       | Estimated AI token cost in USD for pipeline events                                |

### Action Types

The following action types are recognized. The `action` field in every event
must be one of these values. The platform rejects events with unrecognized
action types.

#### Authentication

| Action        | Description                       |
| ------------- | --------------------------------- |
| `auth.login`  | A user successfully authenticated |
| `auth.logout` | A user ended their session        |
| `auth.failed` | An authentication attempt failed  |

#### License Management

| Action           | Description                     |
| ---------------- | ------------------------------- |
| `license.create` | A new license was issued        |
| `license.revoke` | An existing license was revoked |
| `license.rotate` | A license key was rotated       |

#### Billing

| Action              | Description                      |
| ------------------- | -------------------------------- |
| `billing.subscribe` | A new subscription was started   |
| `billing.cancel`    | A subscription was cancelled     |
| `billing.upgrade`   | A subscription tier was upgraded |

#### Team Administration

| Action             | Description                             |
| ------------------ | --------------------------------------- |
| `team.invite`      | A user was invited to a team            |
| `team.remove`      | A user was removed from a team          |
| `team.role_change` | A user's role within a team was changed |

#### Credentials

| Action           | Description                       |
| ---------------- | --------------------------------- |
| `api_key.create` | An API key was created            |
| `api_key.revoke` | An API key was revoked            |
| `webhook.create` | A webhook endpoint was registered |
| `webhook.delete` | A webhook endpoint was removed    |

#### Pipeline Activity

| Action               | Description                                   |
| -------------------- | --------------------------------------------- |
| `pipeline.started`   | An automated pipeline run began               |
| `pipeline.completed` | A pipeline run finished successfully          |
| `pipeline.failed`    | A pipeline run ended in failure               |
| `stage.started`      | A single pipeline stage began execution       |
| `stage.completed`    | A single pipeline stage finished successfully |
| `stage.failed`       | A single pipeline stage failed                |

#### Source Control

| Action           | Description                              |
| ---------------- | ---------------------------------------- |
| `commit.created` | A git commit was created by the pipeline |
| `pr.created`     | A pull request was opened                |
| `pr.merged`      | A pull request was merged                |

#### Cost and Skills

| Action          | Description                                            |
| --------------- | ------------------------------------------------------ |
| `cost.recorded` | AI token cost was recorded for a pipeline run          |
| `skill.invoked` | A pipeline skill (stage instruction file) was executed |

#### Compliance Operations

| Action                        | Description                                             |
| ----------------------------- | ------------------------------------------------------- |
| `audit.purged`                | Audit records were deleted under a retention policy     |
| `compliance.report.generated` | A compliance report was created                         |
| `compliance.report.scheduled` | A compliance report was scheduled for future generation |

---

## Security Architecture

### Authentication and Authorization

The audit submission API (`POST /api/v1/audit/events`) requires a Bearer API
key in the `Authorization` header. API keys are:

- Scoped to a specific organization account
- Rotatable without disrupting event flow (configure the new key, then revoke
  the old one)
- Recorded in the audit log themselves when created or revoked (`api_key.create`,
  `api_key.revoke`)

The audit query API (`GET /api/v1/audit/events`) requires an authenticated
user session token. Unauthorized requests receive `401 Unauthorized`. Requests
by users without access to audit data receive `403 Forbidden`. In both cases
the VSCode Audit Log Viewer displays a "no access" state rather than an error.

### Transport Security

All communication between the SDK client and the platform API uses HTTPS with
TLS 1.2 or higher. Plaintext HTTP connections are not accepted by the platform.

The SDK enforces a configurable request timeout (default: 5 seconds) to prevent
pipeline stalls from slow network conditions.

### Tamper Evidence

The platform's audit log table is **append-only**. There are no API endpoints
or application code paths that allow in-place modification or deletion of
individual audit records.

Deletion is only possible through the `audit.purged` action, which itself
creates an audit record before removing data. This ensures that any data
purge — whether routine (retention policy enforcement) or exceptional — is
itself recorded in the audit trail.

### Offline Queue Integrity

When the platform is unreachable (network outage, planned maintenance), the SDK
writes pending events to a local JSON file:

```
.nightgauge/audit-queue.json
```

This file is stored in the project working directory. When the next pipeline
run completes, the SDK attempts to submit the queued events before completing.
If submission succeeds the queue file is deleted; if it fails the file is
retained for the next attempt.

The queue is capped at a configurable maximum size (default: 10,000 events).
When the cap is reached, the oldest events are discarded first to prevent
unbounded disk growth. The discard action is logged to stderr with an event
count.

### Error Handling: Silent-to-Caller Design

The `AuditEventClient` never throws exceptions to the calling pipeline code.
All errors are written to `stderr` and the pipeline continues uninterrupted.
This design ensures that a transient audit service outage cannot block a
developer's pipeline run.

Errors that cause event discard (not retry) are:

- Platform returns a `4xx` response: indicates a client-side problem (bad
  API key, malformed event). Events are logged to stderr and discarded.
- Zod schema validation failure on the client: the event was malformed before
  transmission. Discarded with a descriptive error message.

Errors that trigger offline queue (retry later):

- Platform returns a `5xx` response: server-side error.
- Network timeout or connection failure.
- `platformUrl` or `apiKey` not configured in the project.

### Sensitive Data Handling

The API key used for audit submission (`audit.apiKey` in config) is treated as
a secret:

- It must not be committed to source control.
- It is typically provided via the `NIGHTGAUGE_AUDIT_API_KEY` environment
  variable or a secrets manager.
- It is transmitted in request headers, not in the event payload, so it does
  not appear in audit records.

---

## Retention Policy

### Platform-Side Retention

Server-side retention is managed by the Nightgauge platform. The platform
enforces configurable retention periods by organization tier. When records are
purged under a retention policy, the platform emits an `audit.purged` event
that identifies the quantity of records deleted and the applicable time window.
This ensures the purge itself is auditable.

### Client-Side Queue Retention

The offline queue file on disk retains events until they are successfully
submitted or until the queue size cap is reached.

| Parameter              | Default                        | Config Key                  | Env Variable                              |
| ---------------------- | ------------------------------ | --------------------------- | ----------------------------------------- |
| Offline queue size cap | 10,000 events                  | `audit.offlineQueueMaxSize` | `NIGHTGAUGE_AUDIT_OFFLINE_QUEUE_MAX_SIZE` |
| Queue file location    | `.nightgauge/audit-queue.json` | `audit.offlineQueuePath`    | `NIGHTGAUGE_AUDIT_OFFLINE_QUEUE_PATH`     |

### Configurable Parameters Summary

The following parameters control event submission behavior. They do not
directly control platform-side retention (which is managed by the platform
independently).

| Parameter      | Default    | Config Key               | Description                                                                                        |
| -------------- | ---------- | ------------------------ | -------------------------------------------------------------------------------------------------- |
| Batch size     | 100 events | `audit.batchSize`        | Events are batched before transmission. A flush occurs when the in-memory batch reaches this size. |
| Flush interval | 30,000 ms  | `audit.flushIntervalMs`  | Periodic flush interval while a pipeline is running.                                               |
| HTTP timeout   | 5,000 ms   | `audit.timeoutMs`        | Maximum wait time for a platform response.                                                         |
| Max retries    | 3          | `audit.retryMaxAttempts` | Maximum number of submission attempts before writing to the offline queue.                         |
| Retry backoff  | 1,000 ms   | `audit.retryBackoffMs`   | Initial delay between retry attempts (exponential backoff).                                        |

---

## Cross-Repository Event Correlation

When teams use Nightgauge across multiple repositories, events from all
repositories flow into the same platform audit log under the same organization
account. Each event carries:

- **`userId`** — identifies which person or service account triggered the event
- **`resourceType`** and **`resourceId`** — identifies which repository or
  resource was involved
- **`timestamp`** — allows reconstruction of activity sequences across repos

The diagram below shows how events from two separate repositories converge into
the same audit log.

```
  Repository A (e.g., nightgauge)          Repository B (e.g., acme-platform)
  ┌─────────────────────────────────┐           ┌─────────────────────────────────────────┐
  │  Pipeline Run (Issue #1584)      │           │  Platform API Change                    │
  │                                 │           │                                         │
  │  pipeline.started               │           │  api_key.create                         │
  │  stage.started (feature-dev)    │           │  webhook.create                         │
  │  stage.completed (feature-dev)  │           │  pr.created                             │
  │  commit.created                 │           │                                         │
  │  pr.created                     │           └──────────────────┬──────────────────────┘
  │  pipeline.completed             │                              │
  └──────────────────┬──────────────┘                              │
                     │                                             │
                     └──────────────────┬──────────────────────────┘
                                        │ HTTPS POST /api/v1/audit/events
                                        ▼
                           ┌────────────────────────────┐
                           │  Platform Audit Log         │
                           │  (single unified table)     │
                           │                             │
                           │  Queryable by:              │
                           │  • userId                   │
                           │  • action type              │
                           │  • resourceType/resourceId  │
                           │  • time window              │
                           └────────────────────────────┘
```

A compliance reviewer can filter the audit log by `resourceType=repository` and
a specific `resourceId` to see all events for a single repository, or remove
the filter to see all events across the organization in chronological order.

---

## Compliance Mapping

The following table maps Nightgauge audit capabilities to specific controls
in SOC 2 Type II Trust Services Criteria and ISO 27001 Annex A.

### SOC 2 Trust Services Criteria (Security)

| Control ID | Criterion                                                                        | How Nightgauge Satisfies It                                                                                                   |
| ---------- | -------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| CC6.1      | Logical access security — restrict access to authorized users                    | `auth.login`, `auth.failed` events; `api_key.create`, `api_key.revoke` events; access token authentication on audit query API |
| CC6.2      | Prior to issuing system credentials, registration and authorization are required | `auth.login` events record successful authentication; `auth.failed` events record rejected attempts                           |
| CC6.3      | Access to information assets is removed when no longer required                  | `team.remove`, `api_key.revoke`, `license.revoke` events                                                                      |
| CC6.6      | Logical access security — restrict unauthorized access                           | `auth.failed` events enable detection of brute-force patterns; API key-based submission authentication                        |
| CC7.2      | The entity monitors system components for anomalies                              | Audit log queryable by time window and action type; Audit Log Viewer in VSCode Dashboard                                      |
| CC8.1      | Change management — authorize, design, develop, configure                        | `pipeline.started`, `pipeline.completed`, `commit.created`, `pr.created`, `pr.merged` events track all code changes           |
| CC9.1      | Risk assessment — identify risks                                                 | `pipeline.failed`, `stage.failed` events provide evidence of failure detection and response                                   |

### SOC 2 Trust Services Criteria (Availability)

| Control ID | Criterion                                                        | How Nightgauge Satisfies It                                       |
| ---------- | ---------------------------------------------------------------- | ----------------------------------------------------------------- |
| A1.2       | Environmental protections to avoid or mitigate potential impacts | Offline queue ensures events are not lost during platform outages |

### SOC 2 Trust Services Criteria (Confidentiality)

| Control ID | Criterion                             | How Nightgauge Satisfies It                                                           |
| ---------- | ------------------------------------- | ------------------------------------------------------------------------------------- |
| C1.1       | Confidential information is protected | API keys transmitted in headers (not payload); TLS required for all audit API traffic |

### ISO 27001 Annex A Controls

| Control | Title                                          | How Nightgauge Satisfies It                                                                                                          |
| ------- | ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| A.8.15  | Logging                                        | All security-relevant actions produce audit events; events are immutable once written                                                |
| A.8.16  | Monitoring activities                          | Audit Log Viewer provides real-time and historical visibility; CSV export for external SIEM integration                              |
| A.5.33  | Protection of records                          | Append-only storage; purge events are themselves recorded (`audit.purged`)                                                           |
| A.8.5   | Secure authentication                          | `auth.*` events record all authentication activity; multi-factor authentication handled by identity provider (recorded at login)     |
| A.5.18  | Access rights                                  | `team.invite`, `team.remove`, `team.role_change` events provide full access provisioning and de-provisioning history                 |
| A.8.18  | Use of privileged utility programs             | `api_key.create`, `api_key.revoke` provide credential lifecycle visibility                                                           |
| A.5.36  | Compliance with policies, rules, and standards | `compliance.report.generated`, `compliance.report.scheduled` events provide evidence of active compliance monitoring                 |
| A.8.13  | Information backup                             | Offline queue provides resilience for event delivery; platform database backups are covered by platform infrastructure documentation |

---

## Operational Procedures

### Enabling the Audit Trail

The audit trail is **opt-in**. To enable it:

**Option 1: Project configuration file**

Add the following to `.nightgauge/config.yaml` in the project root:

```yaml
audit:
  enabled: true
  platform_url: https://platform.nightgauge.dev
  api_key: sk_live_... # Keep this secret — use a secrets manager
  batch_size: 100 # Optional: events per batch
  flush_interval_ms: 30000 # Optional: flush every 30 seconds
  offline_queue_path: .nightgauge/audit-queue.json # Optional
  offline_queue_max_size: 10000 # Optional: cap for disk-buffered events
```

**Option 2: Environment variables**

```bash
export NIGHTGAUGE_AUDIT_ENABLED=true
export NIGHTGAUGE_AUDIT_PLATFORM_URL=https://platform.nightgauge.dev
export NIGHTGAUGE_AUDIT_API_KEY=sk_live_...
```

Environment variables take precedence over the config file.

### Verifying Event Delivery

To verify that events are reaching the platform:

1. Open the Nightgauge VSCode Dashboard.
2. Click the **Audit Log** tab.
3. Set a date range that covers a recent pipeline run.
4. Confirm that `pipeline.started` and `pipeline.completed` events appear with
   status `success`.

Alternatively, query the platform REST API directly:

```bash
curl -s "https://platform.nightgauge.dev/api/v1/audit/events?from=2024-01-01T00:00:00Z&to=2024-12-31T23:59:59Z&pageSize=10" \
  -H "Authorization: Bearer $NIGHTGAUGE_AUDIT_API_KEY" | jq '.entries[0]'
```

### Verifying Audit Log Integrity

The audit log is append-only. To verify no records have been retroactively
modified or deleted for a given time window:

1. Run the compliance report for the time window
   (`compliance.report.generated` event will be created).
2. Export the audit log for the same time window as CSV.
3. Compare the event count in the report against the CSV row count.
4. Check that no `audit.purged` events appear within the time window under
   review unless expected under the retention policy.

### Exporting Audit Data

**From the VSCode Dashboard:**

1. Open the Audit Log tab.
2. Set the desired date range and apply any action or user filters.
3. Click **Export CSV**.
4. The downloaded file contains all matching events including metadata.

**Via the REST API:**

```bash
# Fetch up to 50 events per page; iterate using page parameter
curl -s "https://platform.nightgauge.dev/api/v1/audit/events?from=2024-01-01T00:00:00Z&to=2024-01-31T23:59:59Z&page=0&pageSize=50" \
  -H "Authorization: Bearer $NIGHTGAUGE_AUDIT_API_KEY"
```

Response format:

```json
{
  "entries": [
    {
      "id": "evt_...",
      "timestamp": "2024-01-15T14:32:10.000Z",
      "userId": "usr_...",
      "userEmail": "developer@example.com",
      "action": "pipeline.completed",
      "resourceType": "pipeline",
      "resourceId": "1584",
      "status": "success",
      "metadata": { "issueNumber": 1584, "branch": "feat/1584-..." },
      "costUsd": 0.042
    }
  ],
  "totalCount": 312,
  "page": 0
}
```

### Generating Compliance Reports

To generate a compliance report for a specific time window:

1. Contact your Nightgauge account administrator or use the platform API
   to trigger report generation.
2. The `compliance.report.generated` event will appear in the audit log with
   `resourceId` set to the report identifier.
3. Scheduled reports emit `compliance.report.scheduled` when enqueued and
   `compliance.report.generated` when produced.

### Investigating a Security Incident

To investigate a specific event or time window:

1. Open the Audit Log tab in the VSCode Dashboard.
2. Use the **User** filter to narrow to a specific actor.
3. Use the **Action** filter to narrow to a specific action category (e.g.,
   `auth.failed`).
4. Use the **Date Range** to bound the time window.
5. Expand individual rows to see full `metadata` and `costUsd` fields.
6. Export filtered results as CSV for offline analysis or submission to auditors.

### Rotating the Audit API Key

If an API key is compromised or needs to be rotated:

1. Generate a new API key through the platform.
2. Update `NIGHTGAUGE_AUDIT_API_KEY` (or `audit.api_key` in config) with
   the new key.
3. Verify event delivery using the procedure above.
4. Revoke the old API key through the platform.

Both the `api_key.create` and `api_key.revoke` events will appear in the audit
log, providing a complete record of the rotation.

---

## Related Documentation

| Topic                      | File                                |
| -------------------------- | ----------------------------------- |
| SDK architecture           | `packages/nightgauge-sdk/README.md` |
| Pipeline execution         | `docs/PIPELINE_EXECUTION.md`        |
| Configuration reference    | `docs/CONFIGURATION.md`             |
| Platform API (server-side) | `../acme-platform/docs/api/`        |
| Security standards         | `standards/security.md`             |
