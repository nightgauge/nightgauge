package adapters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The VS Code extension spawns agent stages itself (runStageSkillHeadless and
// its resume and interactive paths) and applies the same credential allowlist
// to them. Its data is generated from this package, never kept by hand:
//
//   - stageEnvGeneratedTSPath is rendered from stageBaseCredentialEnv,
//     openCodePlatformEnvNames, openCodeCatalogEnvNames, adapterCredentialEnv
//     and the credential segment lists, and TestStageEnvGeneratedTSCurrent
//     fails when the committed file differs;
//   - stageEnvDecisionsPath records this package's decision for every adapter
//     over a corpus of names, and the extension's stageCredentialEnv test
//     fails when its rule decides any of them differently.
//
// Regenerate both with:
//
//	NIGHTGAUGE_UPDATE_GOLDEN=1 go test ./internal/execution/adapters/ -run 'TestStageEnv(GeneratedTSCurrent|DecisionsGolden)'
const (
	stageEnvGeneratedTSPath = "packages/nightgauge/src/utils/stageCredentialEnv.generated.ts"
	stageEnvDecisionsPath   = "testdata/stage_env_decisions.json"
)

// repoRootFromAdapters is the repository root, relative to this package.
const repoRootFromAdapters = "../../.."

func TestStageEnvGeneratedTSCurrent(t *testing.T) {
	t.Parallel()
	checkOrUpdateGolden(t, filepath.Join(repoRootFromAdapters, stageEnvGeneratedTSPath), renderStageEnvTS())
}

func TestStageEnvDecisionsGolden(t *testing.T) {
	t.Parallel()
	checkOrUpdateGolden(t, stageEnvDecisionsPath, renderStageEnvDecisions(t))
}

func checkOrUpdateGolden(t *testing.T, path string, want []byte) {
	t.Helper()
	if os.Getenv("NIGHTGAUGE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (regenerate with NIGHTGAUGE_UPDATE_GOLDEN=1): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date with stage_env.go: regenerate with "+
			"NIGHTGAUGE_UPDATE_GOLDEN=1 go test ./internal/execution/adapters/ "+
			"-run 'TestStageEnv(GeneratedTSCurrent|DecisionsGolden)'", path)
	}
}

func tsStringArray(b *strings.Builder, indent string, names []string) {
	if len(names) == 0 {
		b.WriteString("[]")
		return
	}
	b.WriteString("[\n")
	for _, n := range names {
		fmt.Fprintf(b, "%s  %q,\n", indent, n)
	}
	b.WriteString(indent + "]")
}

func renderStageEnvTS() []byte {
	var b strings.Builder
	b.WriteString(`/**
 * stageCredentialEnv.generated.ts — GENERATED. DO NOT EDIT.
 *
 * Source:    internal/execution/adapters/stage_env.go (and the OpenCode catalog it reads)
 * Generator: TestStageEnvGeneratedTSCurrent in internal/execution/adapters/stage_env_ts_test.go
 *
 * The data of the stage environment's credential allowlist (ADR-023
 * Decision 7), for the stages this extension spawns itself. The rule that reads
 * it is stageCredentialEnv.ts. Regenerate with:
 *
 *   NIGHTGAUGE_UPDATE_GOLDEN=1 go test ./internal/execution/adapters/ -run 'TestStageEnv(GeneratedTSCurrent|DecisionsGolden)'
 */

/** The operator's extra stage credentials, a comma-separated list of names. */
export const STAGE_ENV_ALLOW_VAR = `)
	fmt.Fprintf(&b, "%q;\n\n", StageEnvAllowVar)

	b.WriteString("/** Credential-named variables every stage keeps (stageBaseCredentialEnv). */\n")
	b.WriteString("export const STAGE_BASE_CREDENTIAL_ENV: readonly string[] = ")
	tsStringArray(&b, "", slices.Sorted(maps.Keys(stageBaseCredentialEnv)))
	b.WriteString(";\n\n")

	b.WriteString("/** Cloud platform variables every stage keeps (openCodePlatformEnvNames). */\n")
	b.WriteString("export const STAGE_PLATFORM_ENV: readonly string[] = ")
	tsStringArray(&b, "", slices.Sorted(maps.Keys(openCodePlatformEnvNames)))
	b.WriteString(";\n\n")

	b.WriteString("/** Every model service key the OpenCode catalog binds (openCodeCatalogEnvNames). */\n")
	b.WriteString("export const STAGE_CATALOG_ENV: readonly string[] = ")
	tsStringArray(&b, "", slices.Sorted(maps.Keys(openCodeCatalogEnvNames)))
	b.WriteString(";\n\n")

	b.WriteString("/** A name's final segment that marks it as a credential. */\n")
	b.WriteString("export const CREDENTIAL_FINAL_SEGMENTS: readonly string[] = ")
	tsStringArray(&b, "", credentialFinalSegments)
	b.WriteString(";\n\n")

	b.WriteString("/** A segment anywhere in a name that marks it as a credential. */\n")
	b.WriteString("export const CREDENTIAL_ANY_SEGMENTS: readonly string[] = ")
	tsStringArray(&b, "", credentialAnySegments)
	b.WriteString(";\n\n")

	b.WriteString(`/** What one adapter's harness authenticates with (adapterCredentialEnv). */
export interface AdapterCredentialEnvDecl {
  readonly names: readonly string[];
  readonly prefixes: readonly string[];
  /** The adapter decides OpenCode catalog variables itself. */
  readonly catalogOwn: boolean;
}

/** The credentials each adapter's harness reads; an unlisted adapter gets the base set only. */
export const ADAPTER_CREDENTIAL_ENV: Readonly<Record<string, AdapterCredentialEnvDecl>> = {
`)
	for _, name := range slices.Sorted(maps.Keys(adapterCredentialEnv)) {
		d := adapterCredentialEnv[name]
		fmt.Fprintf(&b, "  %q: {\n    names: ", name)
		tsStringArray(&b, "    ", d.names)
		b.WriteString(",\n    prefixes: ")
		tsStringArray(&b, "    ", d.prefixes)
		fmt.Fprintf(&b, ",\n    catalogOwn: %t,\n  },\n", d.catalogOwn)
	}
	b.WriteString("};\n")
	return []byte(b.String())
}

// stageEnvDecisionCorpus is every name whose decision the golden records: the
// test environment's names, every catalog and platform variable, and names
// that probe each branch of the rule.
func stageEnvDecisionCorpus() []string {
	set := map[string]bool{}
	for k := range parentEnv {
		set[k] = true
	}
	for k := range openCodeCatalogEnvNames {
		set[k] = true
	}
	for k := range openCodePlatformEnvNames {
		set[k] = true
	}
	for k := range stageBaseCredentialEnv {
		set[k] = true
	}
	for _, d := range adapterCredentialEnv {
		for _, n := range d.names {
			set[n] = true
		}
		for _, p := range d.prefixes {
			set[p+"API_KEY"] = true
			set[p+"MODEL"] = true
		}
	}
	for _, k := range []string{
		"my_api_key", "github_token", "Some_Secret_Path", "CLIENT_SECRET_PATH",
		"SENTRY_DSN", "HF_PAT", "MY_APIKEY", "GOOGLE_APPLICATION_CREDENTIALS",
		"CLAUDE_CODE_MAX_OUTPUT_TOKENS", "GIT_ASKPASS", "KEYCHAIN_PATH",
		"OPENCODE_CONFIG", "OPENCODE_SERVER_PASSWORD", StageEnvAllowVar,
		"NIGHTGAUGE_LM_STUDIO_API_KEY", "NIGHTGAUGE_OPENAI_COMPATIBLE_API_KEY",
		"TOKEN", "KEY", "_", "",
	} {
		set[k] = true
	}
	return slices.Sorted(maps.Keys(set))
}

type stageEnvDecisionCase struct {
	Adapter    string   `json:"adapter"`
	ExtraAllow []string `json:"extra_allow"`
	// Kept is every corpus name the stage keeps; the rest are withheld.
	Kept []string `json:"kept"`
}

func renderStageEnvDecisions(t *testing.T) []byte {
	t.Helper()
	corpus := stageEnvDecisionCorpus()
	adaptersList := append(slices.Sorted(maps.Keys(adapterCredentialEnv)), "unlisted-adapter")
	var cases []stageEnvDecisionCase
	for _, a := range adaptersList {
		for _, extra := range [][]string{{}, {"IB_VERIFY_PASSWORD", "OPENAI_API_KEY", "NPM_TOKEN"}} {
			kept := []string{}
			for _, k := range corpus {
				if !StageWithholdsCredentialEnv(a, k, extra) {
					kept = append(kept, k)
				}
			}
			cases = append(cases, stageEnvDecisionCase{Adapter: a, ExtraAllow: extra, Kept: kept})
		}
	}
	out, err := json.MarshalIndent(struct {
		Comment string                 `json:"_comment"`
		Corpus  []string               `json:"corpus"`
		Cases   []stageEnvDecisionCase `json:"cases"`
	}{
		Comment: "GENERATED by TestStageEnvDecisionsGolden (stage_env_ts_test.go). " +
			"StageWithholdsCredentialEnv's decision over the corpus; the extension's rule must agree.",
		Corpus: corpus,
		Cases:  cases,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}
