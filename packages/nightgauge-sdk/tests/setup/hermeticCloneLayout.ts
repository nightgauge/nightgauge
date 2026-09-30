/**
 * Hermetic clone layout for every SDK test file (ADR-024 § 7).
 *
 * The suite runs with its working directory inside this checkout, so a
 * default per-clone path (ContextManager(), RunStateManager(), the analysis
 * defaults, ...) would resolve into the real clone's git directory. This setup
 * file pins the working directory's layout to a per-file temporary directory
 * before any test runs. Tests that exercise resolution itself use their own
 * temporary repositories and are unaffected.
 */
import { afterAll } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { cloneLayoutFor, setCloneLayout } from "../../src/context/cloneLayout.js";

const gitCommonDir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "ng-sdk-git-")));
const cwd = path.resolve(process.cwd());
setCloneLayout(cwd, cloneLayoutFor(cwd, gitCommonDir));

afterAll(() => {
  fs.rmSync(gitCommonDir, { recursive: true, force: true });
});
