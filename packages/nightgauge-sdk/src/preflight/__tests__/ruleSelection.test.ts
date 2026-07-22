import { describe, expect, it } from "vitest";
import { selectRule } from "../ruleSelection.js";
import type { RuleEvaluator } from "../types.js";

describe("selectRule", () => {
  it("returns null when no rule applies", () => {
    expect(selectRule("a totally generic acceptance criterion text")).toBeNull();
  });

  it("matches file-exists for a path-like AC mentioning presence", () => {
    const sel = selectRule("New file `docs/CONTEXT_ARCHITECTURE.md` exists");
    expect(sel).not.toBeNull();
    expect(sel?.rule.name).toBe("file-exists");
    expect(sel?.extracted.path).toBe("docs/CONTEXT_ARCHITECTURE.md");
  });

  it("matches grep-for-symbol when AC names a declaration verb", () => {
    const sel = selectRule("Function reconcileAcceptanceCriteria added to SDK");
    expect(sel?.rule.name).toBe("grep-for-symbol");
    expect(sel?.extracted.symbol).toBe("reconcileAcceptanceCriteria");
  });

  it("matches npm-script-defined for `npm run X` references", () => {
    const sel = selectRule("`npm run build` succeeds");
    expect(sel?.rule.name).toBe("npm-script-defined");
    expect(sel?.extracted.script).toBe("build");
  });

  it("matches workflow-job-named for workflow + job", () => {
    const sel = selectRule("The workflow .github/workflows/ci.yml has job `lint`");
    expect(sel?.rule.name).toBe("workflow-job-named");
    expect(sel?.extracted.workflow).toBe("ci.yml");
    expect(sel?.extracted.job).toBe("lint");
  });

  it("matches doc-section-present for documented section", () => {
    const sel = selectRule(
      "Documented in `docs/CONTEXT_ARCHITECTURE.md` section `Schema Versioning`"
    );
    expect(sel?.rule.name).toBe("doc-section-present");
  });

  it("matches branch-protection-rule-present", () => {
    const sel = selectRule("Branch protection on `main` requires required check `build`");
    expect(sel?.rule.name).toBe("branch-protection-rule-present");
    expect(sel?.extracted.requiredCheck).toBe("build");
  });

  it("respects custom rule order — first match wins", () => {
    const calls: string[] = [];
    const r1: RuleEvaluator = {
      name: "first",
      applies(t: string) {
        calls.push("first");
        return t.length > 0 ? { ok: "yes" } : null;
      },
      async evaluate() {
        return { classification: "satisfied", reason: "" };
      },
    };
    const r2: RuleEvaluator = {
      name: "second",
      applies(_t: string) {
        calls.push("second");
        return { ok: "yes" };
      },
      async evaluate() {
        return { classification: "satisfied", reason: "" };
      },
    };
    const sel = selectRule("anything", [r1, r2]);
    expect(sel?.rule.name).toBe("first");
    expect(calls).toEqual(["first"]);
  });
});
