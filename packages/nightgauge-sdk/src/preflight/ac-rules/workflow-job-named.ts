import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import type { RuleEvaluator } from "../types.js";

const WORKFLOW_RE = /\.github\/workflows\/([A-Za-z0-9_.-]+\.ya?ml)/;
const JOB_RE = /\bjob(?:\s+named)?\s+`([A-Za-z0-9_-]+)`|\bjob:?\s+`([A-Za-z0-9_-]+)`/;

/**
 * Detects whether a job key is defined in the YAML workflow file.
 *
 * We avoid pulling in a YAML parser by matching the canonical
 * `jobs:` -> indented `<jobName>:` shape that GitHub Actions enforces.
 */
function workflowDefinesJob(content: string, jobName: string): boolean {
  const lines = content.split(/\r?\n/);
  let inJobs = false;
  let jobsIndent = 0;
  for (const line of lines) {
    if (/^\s*jobs:\s*$/.test(line)) {
      inJobs = true;
      jobsIndent = line.indexOf("jobs:");
      continue;
    }
    if (!inJobs) continue;
    const trimmed = line.replace(/\s+$/, "");
    if (trimmed.length === 0) continue;
    const lineIndent = trimmed.length - trimmed.replace(/^\s+/, "").length;
    if (lineIndent <= jobsIndent && /^\s*\S/.test(trimmed)) {
      // We exited the jobs block (e.g., a sibling top-level key).
      inJobs = false;
      continue;
    }
    const m = /^\s+([A-Za-z0-9_-]+):/.exec(trimmed);
    if (m && m[1] === jobName) {
      return true;
    }
  }
  return false;
}

const workflowJobNamedRule: RuleEvaluator = {
  name: "workflow-job-named",

  applies(text: string) {
    const wf = WORKFLOW_RE.exec(text);
    if (!wf) return null;
    const job = JOB_RE.exec(text);
    if (!job) return null;
    const jobName = job[1] ?? job[2];
    if (!jobName) return null;
    return { workflow: wf[1], job: jobName };
  },

  async evaluate(ctx) {
    const workflowsDir = path.join(ctx.workdir, ".github", "workflows");
    const targetFile = ctx.extracted.workflow;
    const jobName = ctx.extracted.job;
    let entries;
    try {
      entries = await readdir(workflowsDir, { withFileTypes: true });
    } catch {
      return {
        classification: "unsatisfied",
        reason: `No .github/workflows directory under ${ctx.workdir}`,
        evidence: [],
      };
    }
    const match = entries.find((e) => e.isFile() && e.name === targetFile);
    if (!match) {
      return {
        classification: "unsatisfied",
        reason: `Workflow file not found: .github/workflows/${targetFile}`,
        evidence: [],
      };
    }
    const fullPath = path.join(workflowsDir, targetFile);
    const content = await readFile(fullPath, "utf-8");
    if (workflowDefinesJob(content, jobName)) {
      return {
        classification: "satisfied",
        reason: `Workflow .github/workflows/${targetFile} defines job \`${jobName}\``,
        evidence: [`.github/workflows/${targetFile}`],
      };
    }
    return {
      classification: "unsatisfied",
      reason: `Workflow .github/workflows/${targetFile} does not define job \`${jobName}\``,
      evidence: [],
    };
  },
};

export default workflowJobNamedRule;
