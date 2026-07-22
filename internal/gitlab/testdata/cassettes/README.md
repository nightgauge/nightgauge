# GitLab Adapter Cassette Fixtures

Static JSON fixtures matching the shape of GitLab REST API responses. Loaded by
unit tests via `os.ReadFile` and replayed through `httptest.Server` handlers —
no live network or VCR-style playback. See ADR-001 in
`.nightgauge/knowledge/features/3365-*/decisions.md` for the rationale
behind handcrafted fixtures vs. recorded ones.

## Layout

```
cassettes/
├── issues/   — IssueService responses (get, list, create, close)
├── mrs/      — PRService (MR) responses (get, list, create)
├── board/    — BoardService responses (get item, list items)
├── ci/       — CIService responses (pipelines, jobs, run logs)
├── rulesets/ — RulesetService responses (push rules, MR approvals)
├── labels/   — LabelService responses (list — for ErrUnsupported fallback)
└── auth/     — Auth-related responses (license probe for edition detection)
```

## Conventions

- **Numeric IDs are fixed** — issue IID 42, project ID 5, label IDs deterministic
- **Labels are arrays of strings** — GitLab REST canonical shape
- **No timestamp fields** — unless required by the consumer; when present they
  are fixed strings (`"2026-01-01T00:00:00Z"`) so tests stay deterministic
- **Max 5 KB per file** — fixtures should encode one entity (or a 2–3 element
  page) per file
- **`web_url` uses `https://gitlab.example.com/...`** — never a real host

## Adding a New Cassette

1. Capture the real response shape from GitLab API docs or an integration test
2. Trim to the minimum the assertion requires
3. Replace timestamps with fixed strings (or remove the field entirely)
4. Save under `<service>/<method-slug>.json`
5. Reference it from a unit test by reading the file and serving it from a
   `stubGitLabServer.handle()` call
