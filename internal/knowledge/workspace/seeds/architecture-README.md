# architecture/

Workspace-level architecture knowledge. Ecosystem-level structural decisions
and topology that span multiple repos.

**What belongs here:**

- Ecosystem topology maps (which repo does what)
- Cross-repo architectural principles (e.g., Go vs. TypeScript split)
- Determinism and self-improvement loop boundaries

**What does NOT belong here:**

- Per-repo architecture details (→ `{repo}/docs/ARCHITECTURE.md`)
- Per-issue implementation decisions (→ `features/{N}-{slug}/decisions.md`)
- Runbooks or operational procedures (→ repo-topic `runbooks/` tier)

**Wiki-link access:** `[[architecture:ecosystem-topology]]` resolves to
`.nightgauge/knowledge/architecture/ecosystem-topology.md` relative to
the workspace root.

Seeded by `nightgauge knowledge workspace-init`.
