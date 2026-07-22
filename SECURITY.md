# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| Latest  | Yes       |

## Reporting a Vulnerability

If you discover a security vulnerability in Nightgauge, please report it
responsibly. **Do not open a public GitHub issue.**

### How to Report

**Preferred:** use GitHub **Private Vulnerability Reporting** — the "Report a
vulnerability" button on this repository's
[Security tab](https://github.com/nightgauge/nightgauge/security/advisories/new).
It opens a private advisory visible only to you and the maintainers, keeps the
whole exchange in one place, and issues a CVE/GHSA on fix.

**Fallback:** email **security@nightgauge.dev**.

Please include:

1. A description of the vulnerability
2. Steps to reproduce (or a proof of concept)
3. The affected component (VSCode extension, SDK, Go binary, skills)
4. Any potential impact assessment

### What to Expect

- **Acknowledgement** within 3 business days (solo-maintained project — see [SUPPORT.md](SUPPORT.md))
- **Status update** within 7 business days
- **Fix timeline** communicated once the issue is triaged

### Coordinated disclosure

We follow a **90-day coordinated-disclosure window**: we ask that you give us up
to 90 days from your report to ship a fix before any public disclosure, and we
will work to be faster than that. We will coordinate the disclosure timing with
you and credit you in the published GHSA advisory (unless you prefer anonymity).

### Safe harbor

We will **not pursue or support legal action** against anyone who, in good
faith, discovers and reports a vulnerability through the channels above, so long
as you:

- make a good-faith effort to avoid privacy violations, data destruction, and
  service interruption;
- only interact with accounts you own or have explicit permission to test, and
  do not access, modify, or retain others' data;
- give us reasonable time to remediate before disclosing; and
- do not exploit the issue beyond the minimum needed to demonstrate it.

Activity conducted consistently with this policy is considered authorized, and
we will help clarify if you are unsure whether a specific action is in scope.

### No paid bounty

Nightgauge does **not** run a paid bug-bounty program. Valid reports are
recognized with credit in the GHSA advisory and our thanks — not payment. Please
report because you want the software to be safe, not for a reward.

## Security Practices

- All dependencies are monitored by Dependabot for known vulnerabilities
- See [standards/security.md](standards/security.md) for coding standards
- See [docs/SECURITY.md](docs/SECURITY.md) for prompt injection safeguards

## Scope

This policy covers the `nightgauge/nightgauge` repository, including:

- VSCode extension (`packages/nightgauge-vscode/`)
- SDK (`packages/nightgauge-sdk/`)
- Go binary (`cmd/nightgauge/`, `internal/`)
- Pipeline skills (`skills/`)

## Threat Model

Nightgauge runs AI agents with a high degree of automation over your
repository — up to and including merging pull requests. Before running it
anywhere that accepts input from strangers, understand the primary attack
path and what does (and does not) mitigate it.

### The primary attack path: prompt injection via work items

GitHub issue bodies, comments, and PR review feedback are **untrusted input**
that pipeline agents read and act on. A crafted issue could attempt to steer
an agent into writing malicious code, exfiltrating secrets, or approving its
own changes. Headless pipeline stages run the AI CLI with permission prompts
disabled — automation is the product — so the model itself is not the
security boundary.

### Mitigations in place

- **Claude command gates** — on the Claude plugin path, a PreToolUse hook parses
  `git`/`gh` argv and blocks known destructive patterns, privilege escalation,
  force pushes, and pushes to `main`. These hooks do not wrap every provider.
  In particular, autonomous Codex execution may use
  `--dangerously-bypass-approvals-and-sandbox`; run it only in an externally
  isolated environment appropriate for the repository's trust level. See
  [docs/SECURITY.md](docs/SECURITY.md).
- **Hard stage gates** — build and test verification cannot be bypassed by
  the agent (`--auto-pass` does not skip build verification), and `pr-merge`
  requires CI green before merging.
- **Human approval points** — the planning stage requires plan approval by
  default, and every gate can be tightened per repo (see
  [docs/STAGE_GATES.md](docs/STAGE_GATES.md) and
  [docs/GATE_RELAXATION.md](docs/GATE_RELAXATION.md)).
- **Optional input sanitization** — prompt content can be screened for
  injection markers before reaching the model (disabled by default; see
  [docs/SECURITY.md](docs/SECURITY.md)).

### Residual risk — our honest recommendations

- **Treat issue and PR content as untrusted.** Run the full autonomous
  pipeline (especially auto-merge) only on repositories where issue creation
  is limited to people you trust. For public repos, keep a human on the merge
  gate.
- **Least privilege**: use a fine-grained GitHub token scoped to the target
  repository, not a broad classic PAT.
- **A blocklist is not a sandbox.** Provider-specific gates cannot enumerate
  every harmful command and are absent on some paths. The agent runs with the
  permissions of its process. For hostile-input scenarios, use a disposable
  container or VM and a least-privilege forge credential.
- Found a way around a gate? That is in scope — report it via the process
  above.
