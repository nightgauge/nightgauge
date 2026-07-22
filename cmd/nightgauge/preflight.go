package main

import (
	"fmt"
	"os"
	"strconv"

	docspkg "github.com/nightgauge/nightgauge/internal/docs"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/scan"
	"github.com/spf13/cobra"
)

// preflightCmd is the top-level "preflight" command. It groups deterministic
// pre-submission gate verbs that replace the bash + grep + python3 + sed
// chains in skills/pr-preflight/SKILL.md (audit row B40, skill-survey rows
// 57-60).
//
// Exit-code semantics differ from sibling `scan` and `docs` verbs by design:
// preflight is a gate. When findings exist, the verb exits 1 so a CI step
// like `nightgauge preflight links --root . || exit 1` does the right
// thing. The underlying `scan` verbs return counts (always exit 0) because
// they're scoring inputs to a rubric, not blocking submission.
func preflightCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Pre-submission gates (links, syntax, secrets, skill-no-direct-gh, skill-anti-patterns, skill-portability, dependency-guard)",
		Long: `Deterministic pre-submission validation gates. Each subcommand inspects the
working tree for a specific class of defect and exits non-zero when findings
exist, so they can be chained in CI or git pre-push hooks. Replaces the
fragile bash + python3 + sed chains in skills/pr-preflight/SKILL.md
(audit row B40).`,
	}
	cmd.AddCommand(preflightLinksCmd())
	cmd.AddCommand(preflightSyntaxCmd())
	cmd.AddCommand(preflightSecretsCmd())
	cmd.AddCommand(preflightSkillNoDirectGHCmd())
	cmd.AddCommand(preflightSkillAntiPatternsCmd())
	cmd.AddCommand(preflightSkillPortabilityCmd())
	cmd.AddCommand(preflightDependencyGuardCmd())
	cmd.AddCommand(preflightACReconcileCmd())
	return cmd
}

// preflightACReconcileCmd implements `nightgauge preflight ac-reconcile
// <issue-number> --body-file <path> [--workdir <dir>] [--out <path>] [--json]`
// (#193 / Issue #3003). Parses Markdown checkbox acceptance criteria from the
// issue body and classifies each against the working tree via the
// deterministic rule library, writing the ac-reconcile-{N}.json report that
// drives feature-planning's verify-and-close / narrow-scope routing.
//
// Unlike sibling preflight gates this verb is ADVISORY, not blocking: it
// exits 0 whenever the reconciliation ran, regardless of aggregate status —
// the planner consumes the report; an unsatisfied AC is not a submission
// defect. Non-zero exits are reserved for operational errors (unreadable
// body file, unwritable output).
func preflightACReconcileCmd() *cobra.Command {
	var (
		workdir    string
		bodyFile   string
		outPath    string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "ac-reconcile <issue-number>",
		Short: "Deterministically reconcile issue acceptance criteria against the working tree",
		Long: `Parse Markdown checkbox acceptance criteria from the issue body (--body-file)
and classify each against the working tree using the deterministic rule
library (workflow-job-named, npm-script-defined, doc-section-present,
grep-for-symbol, file-exists). Writes the ac-reconcile-{N}.json report
(schema v1.0 — see docs/CONTEXT_ARCHITECTURE.md) whose aggregate_status
drives feature-planning's verify-and-close short-circuit and focus_acs
scope narrowing. Consumes zero LLM tokens.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			if bodyFile == "" {
				return fmt.Errorf("--body-file is required")
			}
			body, err := os.ReadFile(bodyFile)
			if err != nil {
				return fmt.Errorf("read body file: %w", err)
			}
			dir := workdir
			if dir == "" {
				dir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve workdir: %w", err)
				}
			}

			result := preflight.ReconcileACs(dir, issueNumber, string(body))

			if outPath != "" {
				if err := preflight.WriteACReconcile(result, outPath); err != nil {
					return err
				}
			}
			if jsonOutput || outPath == "" {
				if err := printJSON(result); err != nil {
					return err
				}
			} else {
				fmt.Printf("ac-reconcile: %d criteria, aggregate=%s approach=%s (report: %s)\n",
					len(result.AcceptanceCriteria), result.AggregateStatus,
					result.SuggestedRoute.Approach, outPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working tree to evaluate against (default: cwd)")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Path to a file containing the issue body (required)")
	cmd.Flags().StringVar(&outPath, "out", "", "Write the ac-reconcile-{N}.json report to this path")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the report JSON to stdout")
	return cmd
}

// preflightSkillAntiPatternsCmd wraps `internal/preflight.RunSkillAntiPatternsCheck`
// and exits 1 when any skill/supporting file contains one of the three
// mechanically-detectable authoring anti-patterns (nested references,
// backslash paths, missing TOC on long supporting files). Mirrors
// scripts/lint-skills/anti-patterns.sh — same scope, same checks, same
// exit-code semantics. Wired into .github/workflows/lint.yml so CI guards
// against skill-layer regressions (#3813, epic #3808).
func preflightSkillAntiPatternsCmd() *cobra.Command {
	var (
		jsonOutput bool
		root       string
	)
	cmd := &cobra.Command{
		Use:   "skill-anti-patterns",
		Short: "Fail when a skill file hits a mechanical authoring anti-pattern",
		Long: `Walk the skills/ tree and emit a finding for each occurrence of the three
mechanically-detectable authoring anti-patterns Anthropic warns against
(#3813, epic #3808):

  nested_reference  a supporting file (_includes/, _shared/) directs the agent
                    to read a *further* supporting file — references must be
                    one level deep (Anthropic guidance).
  backslash_path    a path token using Windows '\' separators — skills must be
                    cross-platform and use '/'.
  missing_toc       a supporting file over the line threshold lacks a
                    '## Contents' heading (the established _includes/ convention).

The four judgment-based anti-patterns (time-sensitive info, inconsistent
terminology, options-without-default, magic numbers) are NOT mechanizable
without high false-positive rates — they are covered by the manual sweep in
docs/skills-anti-pattern-sweep.md, not this gate.

Schema version 1 — field names (v, root, files_checked, findings, warnings)
and the check enum are stable and consumed by callers via fixed jq paths.

Exit codes:
  0  no anti-pattern occurrences
  1  one or more findings (gate fails)
  2  hard error (e.g. unresolvable root)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := preflight.RunSkillAntiPatternsCheck(cmd.Context(), preflight.SkillAntiPatternsOptions{
				Root: root,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "preflight skill-anti-patterns: %v\n", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printPreflightSkillAntiPatternsHuman(result)
			}
			if len(result.Findings) > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&root, "root", "", "Repository root (default: current working directory)")
	return cmd
}

func printPreflightSkillAntiPatternsHuman(r *preflight.SkillAntiPatternsResult) {
	fmt.Printf("nightgauge preflight skill-anti-patterns — schema v%d\n", r.V)
	fmt.Printf("root: %s\n", r.Root)
	fmt.Printf("files checked: %d  findings: %d\n", r.FilesChecked, len(r.Findings))
	for _, f := range r.Findings {
		if f.Line > 0 {
			fmt.Printf("  ✗ [%s] %s:%d  %s\n", f.Check, f.File, f.Line, f.Match)
		} else {
			fmt.Printf("  ✗ [%s] %s  %s\n", f.Check, f.File, f.Match)
		}
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
	if len(r.Findings) == 0 {
		fmt.Println("no skill anti-patterns found ✓")
	}
}

// preflightSkillNoDirectGHCmd wraps `internal/preflight.RunSkillNoDirectGHCheck`
// and exits 1 when any skills/*/SKILL.md contains a direct `gh ` token.
// Mirrors scripts/lint-skills/no-direct-gh.sh — same scope, same regex,
// same exit-code semantics. Wired into .github/workflows/lint.yml so CI
// guards against skill-layer regressions across the forge migration
// (#3363, ADR-008).
func preflightSkillNoDirectGHCmd() *cobra.Command {
	var (
		jsonOutput    bool
		root          string
		allowlistPath string
	)
	cmd := &cobra.Command{
		Use:   "skill-no-direct-gh",
		Short: "Fail when any skill SKILL.md contains a direct `gh ` call",
		Long: `Walk skills/*/SKILL.md and emit a finding for each line containing the
\bgh  pattern. Skills target the 'nightgauge forge' abstraction
(ADR-008) — direct gh calls bypass the cross-forge boundary and break
the GitLab matrix slot of the skills-smoke CI workflow.

Schema version 1 — field names (v, root, skills_checked, skills_exempted,
findings, warnings) are stable and consumed by callers via fixed jq paths.

The allowlist (default: scripts/lint-skills/allowlist.txt) lists skill
directory names exempted from the gate — used to track the un-migrated
tail (≤4 gh calls each) from the Wave 4 forge migration. Each entry
MUST eventually be migrated and removed from the allowlist.

Exit codes:
  0  no skill contains a direct gh call (after allowlist filtering)
  1  one or more skills regressed (gate fails)
  2  hard error (e.g. unresolvable root)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := preflight.RunSkillNoDirectGHCheck(cmd.Context(), preflight.SkillNoDirectGHOptions{
				Root:          root,
				AllowlistPath: allowlistPath,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "preflight skill-no-direct-gh: %v\n", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printPreflightSkillNoDirectGHHuman(result)
			}
			if len(result.Findings) > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&root, "root", "", "Repository root (default: current working directory)")
	cmd.Flags().StringVar(&allowlistPath, "allowlist", "", "Path to allowlist file (default: <root>/scripts/lint-skills/allowlist.txt)")
	return cmd
}

func printPreflightSkillNoDirectGHHuman(r *preflight.SkillNoDirectGHResult) {
	fmt.Printf("nightgauge preflight skill-no-direct-gh — schema v%d\n", r.V)
	fmt.Printf("root: %s\n", r.Root)
	fmt.Printf("skills checked: %d  findings: %d  exempted: %d\n",
		r.SkillsChecked, len(r.Findings), len(r.SkillsExempted))
	for _, f := range r.Findings {
		fmt.Printf("  ✗ %s:%d  %s\n", f.SkillFile, f.Line, f.Match)
	}
	for _, name := range r.SkillsExempted {
		fmt.Printf("  · exempted (allowlist): %s\n", name)
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
	if len(r.Findings) == 0 {
		fmt.Println("no direct gh calls found in non-allowlisted skills ✓")
	}
}

// preflightSkillPortabilityCmd wraps `internal/preflight.RunSkillPortabilityCheck`
// and exits 1 when any skill Markdown file embeds a hardcoded VSCode-extension
// binary path. Mirrors scripts/lint-skills/portability.sh. Wired into
// .github/workflows/lint.yml so CI guards skill cross-adapter portability
// (#4029) — skills must resolve the nightgauge binary provider-neutrally
// ($NIGHTGAUGE_BIN → PATH → repo bin → canonical bin → ~/go/bin), never via
// a VSCode-only path that silently fails under Codex/Gemini/etc.
func preflightSkillPortabilityCmd() *cobra.Command {
	var (
		jsonOutput bool
		root       string
	)
	cmd := &cobra.Command{
		Use:   "skill-portability",
		Short: "Fail when any skill embeds a non-portable (VSCode-extension) binary path",
		Long: `Walk every Markdown file under skills/ (SKILL.md plus _includes/ and
_shared/ supporting files) and emit a finding for each line embedding a
hardcoded VSCode-extension binary path
(.vscode/extensions/nightgauge…). Such paths break portability across
the Codex, Gemini, Copilot and Cursor adapters, which never populate the
VSCode extensions directory. Provider-neutral discovery
($NIGHTGAUGE_BIN → PATH → repo bin → canonical-repo bin → ~/go/bin) is
the contract — see skills/_shared/PREFLIGHT.md (#4029).

The Claude-only claude-plugins/.../guard.sh intentionally keeps that glob
(it is not a skill) and is NOT scanned by this gate.

Schema version 1 — field names (v, root, files_checked, findings, warnings)
are stable and consumed by callers via fixed jq paths.

Exit codes:
  0  no skill embeds a non-portable binary path
  1  one or more skills regressed (gate fails)
  2  hard error (e.g. unresolvable root)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := preflight.RunSkillPortabilityCheck(cmd.Context(), preflight.SkillPortabilityOptions{
				Root: root,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "preflight skill-portability: %v\n", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printPreflightSkillPortabilityHuman(result)
			}
			if len(result.Findings) > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&root, "root", "", "Repository root (default: current working directory)")
	return cmd
}

func printPreflightSkillPortabilityHuman(r *preflight.SkillPortabilityResult) {
	fmt.Printf("nightgauge preflight skill-portability — schema v%d\n", r.V)
	fmt.Printf("root: %s\n", r.Root)
	fmt.Printf("files checked: %d  findings: %d\n", r.FilesChecked, len(r.Findings))
	for _, f := range r.Findings {
		fmt.Printf("  ✗ %s:%d  [%s]  %s\n", f.SkillFile, f.Line, f.Check, f.Match)
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
	if len(r.Findings) == 0 {
		fmt.Println("all skills use provider-neutral binary discovery ✓")
	}
}

// preflightLinksCmd wraps `internal/docs.Run` and exits 1 when broken links
// are found. The JSON shape is identical to `docs check-links` so consumers
// that already parse that schema can switch verbs without re-parsing.
func preflightLinksCmd() *cobra.Command {
	var (
		jsonOutput       bool
		root             string
		target           string
		section          string
		excludeTemplates bool
	)
	cmd := &cobra.Command{
		Use:   "links",
		Short: "Validate relative markdown links resolve to real files",
		Long: `Walk --root for *.md files and verify every relative link resolves to an
existing path. Wraps the same walker used by 'docs check-links' (audit row B6)
and shares its stable v1 JSON schema — only exit-code semantics differ.

Exit codes:
  0  scan completed, no broken links
  1  one or more broken links found (gate fails)
  2  hard error (e.g. unresolvable root, target outside root)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := docspkg.Run(cmd.Context(), docspkg.CheckLinksOptions{
				Root:             root,
				Target:           target,
				Section:          section,
				ExcludeTemplates: excludeTemplates,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "preflight links: %v\n", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printPreflightLinksHuman(result)
			}
			if result.LinksBroken > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&root, "root", "", "Directory tree to scan (default: current working directory)")
	cmd.Flags().StringVar(&target, "target", "", "Restrict validation to a single markdown file (relative to --root, or absolute)")
	cmd.Flags().StringVar(&section, "section", "", "Restrict validation to links inside the named heading subtree (case-insensitive)")
	cmd.Flags().BoolVar(&excludeTemplates, "exclude-templates", false, "Skip skill (*/skills/*/SKILL.md) and command (*/claude-plugins/*/commands/*) files")
	return cmd
}

func printPreflightLinksHuman(r *docspkg.CheckLinksResult) {
	fmt.Printf("nightgauge preflight links — schema v%d\n", r.V)
	fmt.Printf("root: %s\n", r.Root)
	fmt.Printf("files: %d  links: %d  broken: %d\n", r.FilesScanned, r.LinksTotal, r.LinksBroken)
	for _, f := range r.Findings {
		fmt.Printf("  ✗ %s:%d  %s  (%s)\n", f.File, f.Line, f.Link, f.Reason)
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// preflightSyntaxCmd validates *.json|*.yaml|*.yml files in --workdir.
func preflightSyntaxCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:   "syntax",
		Short: "Validate JSON and YAML file syntax",
		Long: `Walk --workdir and parse every *.json, *.yaml, and *.yml file. Replaces the
'python3 -m json.tool' and 'python3 -c "import yaml"' chains in
skills/pr-preflight/SKILL.md Checks 2 and 3 (audit row B40, skill-survey
row 58).

Schema version 1 — field names (v, workdir, files_scanned, files_invalid,
findings, warnings) and the format enum (json, yaml) are stable. Skills
parse output via fixed jq paths; any breaking change requires bumping v.

Excluded directories: .git, node_modules, vendor, dist, build, coverage,
.next, out. Files larger than 5 MiB are skipped with a warning.

Exit codes:
  0  all files parsed cleanly
  1  one or more files failed to parse (gate fails)
  2  hard error (e.g. unresolvable workdir)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := preflight.RunSyntaxCheck(cmd.Context(), preflight.SyntaxOptions{
				Workdir: workdir,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "preflight syntax: %v\n", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printPreflightSyntaxHuman(result)
			}
			if result.FilesInvalid > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to scan (default: current working directory)")
	return cmd
}

func printPreflightSyntaxHuman(r *preflight.SyntaxCheckResult) {
	fmt.Printf("nightgauge preflight syntax — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n", r.Workdir)
	fmt.Printf("scanned: %d  invalid: %d\n", r.FilesScanned, r.FilesInvalid)
	for _, f := range r.Findings {
		if f.Line > 0 {
			fmt.Printf("  ✗ [%s] %s:%d  %s\n", f.Format, f.File, f.Line, f.Error)
		} else {
			fmt.Printf("  ✗ [%s] %s  %s\n", f.Format, f.File, f.Error)
		}
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// preflightSecretsCmd wraps `internal/scan.RunSecretsScan` and exits 1 when
// any pattern matched. The scan verb itself exits 0 even with matches because
// it's a counter; preflight is a gate — different audiences, divergent exit
// codes, identical JSON shape (so consumers can re-parse one schema either way).
func preflightSecretsCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Detect committed secrets (gate semantics)",
		Long: `Walk --workdir for the six fixed secret patterns from 'scan secrets'
(generic key/value, PEM private key, AWS access key, JWT/bearer, embedded
connection string, committed .env file). Replaces the inline grep+wc -l
chain in skills/pr-preflight/SKILL.md Check 5 (audit row B40, skill-survey
row 59).

Reuses the v1 schema from 'scan secrets' (audit row B41) — same field names,
same six pattern keys, same regex bank. Only exit code differs:

  scan secrets    — counts matches (exits 0 even when total > 0)
  preflight secrets — gates submission (exits 1 when total > 0)

Exit codes:
  0  no secrets detected
  1  one or more secret-pattern matches found (gate fails)
  2  hard error (e.g. unresolvable workdir)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := scan.RunSecretsScan(cmd.Context(), scan.SecretsOptions{
				Workdir: workdir,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "preflight secrets: %v\n", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printPreflightSecretsHuman(result)
			}
			if result.Total > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to scan (default: current working directory)")
	return cmd
}

func printPreflightSecretsHuman(r *scan.SecretsScanResult) {
	fmt.Printf("nightgauge preflight secrets — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n", r.Workdir)
	patternLabels := []struct{ key, label string }{
		{"generic_kv", "generic key/value"},
		{"pem_private_key", "PEM private key"},
		{"aws_access_key", "AWS access key"},
		{"jwt_bearer", "JWT / bearer token"},
		{"connection_string", "connection string"},
		{"dotenv_files", "committed .env files"},
	}
	for _, p := range patternLabels {
		fmt.Printf("  %-20s %d\n", p.label, r.Patterns[p.key])
	}
	fmt.Printf("\ntotal: %d\n", r.Total)
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}
