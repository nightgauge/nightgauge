// index.js is the file OpenCode 2.x loads from a plugin directory. Observed on
// opencode v2.2.7 (#3302): a directory entry loads <dir>/index.js, and a
// package.json `main` naming any other file is ignored and nothing loads (no
// "loading plugin" log line, no error), so the run would go ungated. The
// plugin itself lives in nightgauge.js; this file only re-exports its
// definition.
export { default } from "./nightgauge.js";
