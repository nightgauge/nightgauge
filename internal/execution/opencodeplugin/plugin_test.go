package opencodeplugin

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/careful"
	"github.com/nightgauge/nightgauge/internal/hometest"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// --- static import scan (Acceptance Criteria: only node:* and ./nightgauge/*) ---

// pluginImportRE matches a static `import ... from "SPEC"` and a dynamic
// `import("SPEC")`, capturing SPEC in whichever group matched.
var pluginImportRE = regexp.MustCompile(`(?:from\s+["']([^"']+)["'])|(?:import\(\s*["']([^"']+)["']\s*\))`)

// scopedImport reports whether file's import of specifier is one this
// plugin may make: a Node builtin, a relative path into ./nightgauge/*, or,
// from inside nightgauge/, a sibling module. Anything else
// (an npm package, an absolute path, a parent-directory escape) is refused.
func scopedImport(file, specifier string) bool {
	if strings.HasPrefix(specifier, "node:") {
		return true
	}
	// A module inside nightgauge/ may import a sibling module by name.
	if strings.HasPrefix(file, "nightgauge/") && siblingModuleRE.MatchString(specifier) {
		return true
	}
	return strings.HasPrefix(specifier, "./nightgauge/") || specifier == "./nightgauge.js"
}

// siblingModuleRE matches "./<name>.js" with no further path segment.
var siblingModuleRE = regexp.MustCompile(`^\./[a-z0-9-]+\.js$`)

// TestPluginImportsAreScoped statically scans every embedded .js file for
// import specifiers: each must be a Node builtin or a ./nightgauge/* sibling,
// so the plugin the binary embeds can never load an npm package. Backing out
// the fix and adding `import z from "zod"` to gates.js (see the task's
// red/green procedure) is exactly what turns this red.
func TestPluginImportsAreScoped(t *testing.T) {
	sub, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	err = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}
		data, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		for _, m := range pluginImportRE.FindAllStringSubmatch(string(data), -1) {
			spec := m[1]
			if spec == "" {
				spec = m[2]
			}
			if !scopedImport(path, spec) {
				t.Errorf("%s imports %q, which is neither a node: builtin nor a ./nightgauge/* sibling", path, spec)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --- golden hook/version parity between plugin.go and nightgauge.js ---

var jsConstStringRE = func(name string) *regexp.Regexp {
	return regexp.MustCompile(name + `\s*=\s*"([^"]*)"`)
}

var jsConstArrayRE = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)` + name + `\s*=\s*\[(.*?)\]`)
}

func extractJSArrayStrings(block string) []string {
	re := regexp.MustCompile(`"([^"]*)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestPluginVersionAndHooksMatchGo compares plugin.go's PluginVersion and
// HookNames against nightgauge.js's own NIGHTGAUGE_PLUGIN_VERSION and
// NIGHTGAUGE_HOOK_NAMES: the two languages must agree, since the sentinel the
// JS side writes is verified against the Go side's constants.
func TestPluginVersionAndHooksMatchGo(t *testing.T) {
	sub, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(sub, "nightgauge.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	vm := jsConstStringRE("NIGHTGAUGE_PLUGIN_VERSION").FindStringSubmatch(src)
	if vm == nil {
		t.Fatal("nightgauge.js has no NIGHTGAUGE_PLUGIN_VERSION constant")
	}
	if vm[1] != PluginVersion {
		t.Errorf("nightgauge.js NIGHTGAUGE_PLUGIN_VERSION = %q, plugin.go PluginVersion = %q", vm[1], PluginVersion)
	}

	am := jsConstArrayRE("NIGHTGAUGE_HOOK_NAMES").FindStringSubmatch(src)
	if am == nil {
		t.Fatal("nightgauge.js has no NIGHTGAUGE_HOOK_NAMES constant")
	}
	got := extractJSArrayStrings(am[1])
	if len(got) != len(HookNames) {
		t.Fatalf("nightgauge.js registers %d hooks %v, plugin.go HookNames has %d %v", len(got), got, len(HookNames), HookNames)
	}
	for i, name := range HookNames {
		if got[i] != name {
			t.Errorf("hook[%d] = %q, want %q (order matters: it is the sentinel's own list)", i, got[i], name)
		}
	}
}

// --- Write ---

func TestWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config", "opencode", "plugin")
	entry, err := Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	// OpenCode 2.x loads a local plugin only from a directory (#3302).
	if entry != dir {
		t.Errorf("entry = %q, want the plugin directory %s", entry, dir)
	}
	for _, rel := range []string{EntryFile, "nightgauge.js", filepath.Join("nightgauge", "gates.js")} {
		p := filepath.Join(dir, rel)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, info.Mode().Perm())
		}
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("plugin dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
}

// --- handshake verifier table ---

func writeSentinel(t *testing.T, path string, s Sentinel) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyLoadedTable(t *testing.T) {
	good := Sentinel{Nonce: "abc123", PluginVersion: PluginVersion, Hooks: HookNames}

	cases := []struct {
		name    string
		write   func(t *testing.T, path string)
		wantErr bool
	}{
		{
			name:    "missing sentinel",
			write:   func(t *testing.T, path string) {},
			wantErr: true,
		},
		{
			name: "wrong nonce",
			write: func(t *testing.T, path string) {
				writeSentinel(t, path, Sentinel{Nonce: "wrong", PluginVersion: PluginVersion, Hooks: HookNames})
			},
			wantErr: true,
		},
		{
			name: "wrong plugin version",
			write: func(t *testing.T, path string) {
				writeSentinel(t, path, Sentinel{Nonce: "abc123", PluginVersion: "999", Hooks: HookNames})
			},
			wantErr: true,
		},
		{
			name: "not JSON",
			write: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "matching sentinel",
			write: func(t *testing.T, path string) {
				writeSentinel(t, path, good)
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".opencode-plugin-run.json")
			tc.write(t, path)
			cfg := HandshakeConfig{Nonce: "abc123", SentinelPath: path, PluginPath: "/plugin/nightgauge.js"}
			err := VerifyLoaded(cfg)
			if tc.wantErr && err == nil {
				t.Fatal("want an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if err != nil {
				var ie *IncompatibleError
				if !asIncompatibleError(err, &ie) {
					t.Fatalf("error %v is not *IncompatibleError", err)
				}
				if ie.Kind() != "adapter_incompatible" {
					t.Errorf("Kind() = %q, want adapter_incompatible", ie.Kind())
				}
				if !strings.Contains(err.Error(), "adapter_incompatible") {
					t.Errorf("Error() = %q must contain the classifier's bare word", err.Error())
				}
			}
		})
	}
}

func asIncompatibleError(err error, target **IncompatibleError) bool {
	if ie, ok := err.(*IncompatibleError); ok {
		*target = ie
		return true
	}
	return false
}

func TestVerifyNotLate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel.json")
	writeSentinel(t, path, Sentinel{Nonce: "n", PluginVersion: PluginVersion, Hooks: HookNames})
	cfg := HandshakeConfig{Nonce: "n", SentinelPath: path}

	// No tool ever ran: always passes.
	if err := VerifyNotLate(cfg, time.Time{}); err != nil {
		t.Errorf("zero firstToolUse must always pass, got %v", err)
	}

	// Written before the first tool call: passes.
	future := time.Now().Add(time.Hour)
	if err := VerifyNotLate(cfg, future); err != nil {
		t.Errorf("sentinel written before firstToolUse must pass, got %v", err)
	}

	// Written after: fails.
	past := time.Now().Add(-time.Hour)
	if err := VerifyNotLate(cfg, past); err == nil {
		t.Error("sentinel written after firstToolUse must fail")
	}

	// Gone at exit: fails.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := VerifyNotLate(cfg, future); err == nil {
		t.Error("a sentinel missing at exit must fail")
	}
}

func TestSentinelPath(t *testing.T) {
	got := SentinelPath("/run/root/output/ctx.json", "/run/root", "run-1")
	want := filepath.Join("/run/root/output", ".opencode-plugin-run-1.json")
	if got != want {
		t.Errorf("SentinelPath with an output file = %q, want %q", got, want)
	}
	got = SentinelPath("", "/run/root", "run-1")
	want = filepath.Join("/run/root", ".opencode-plugin-run-1.json")
	if got != want {
		t.Errorf("SentinelPath with no output file = %q, want %q", got, want)
	}
}

func TestDeleteStaleSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel.json")
	if err := DeleteStaleSentinel(path); err != nil {
		t.Fatalf("deleting a missing sentinel must not error: %v", err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DeleteStaleSentinel(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stale sentinel was not removed")
	}
}

func TestHandshakeConfigFromEnv(t *testing.T) {
	if _, ok := HandshakeConfigFromEnv(map[string]string{}); ok {
		t.Error("an empty env must yield ok=false")
	}
	env := map[string]string{
		EnvNonce:      "n",
		EnvSentinel:   "/s.json",
		EnvPluginPath: "/p.js",
	}
	cfg, ok := HandshakeConfigFromEnv(env)
	if !ok || cfg.Nonce != "n" || cfg.SentinelPath != "/s.json" || cfg.PluginPath != "/p.js" {
		t.Errorf("HandshakeConfigFromEnv = %+v, %v", cfg, ok)
	}
}

// --- Node harness: drives the plugin the way OpenCode 2.x's plugin host
// would, without spawning opencode itself (opencode has no headless "call this
// hook" mode). Fails, never skips, under CI without node.

// fakeHostJS is a stand-in for OpenCode 2.x's plugin host (#3302), shaped on
// what a probe observed opencode v2.2.7 hand a plugin's setup:
// ctx.location.directory, ctx.tool.hook(name, fn), ctx.session.hook(name, fn)
// and ctx.event.subscribe({types}) returning an async iterable. loadPlugin
// refuses anything but a {id, setup} definition, as 2.2.7's loader does, and
// returns the captured registrations. emit(evt) resolves once the plugin's
// event loop has finished handling evt (the iterator resumes only after the
// handler's promise settles), so a test reads the event's effects after it.
const fakeHostJS = `
async function loadPlugin(def, cwd) {
  if (!def || typeof def !== "object" || typeof def.id !== "string" || typeof def.setup !== "function") {
    throw new Error("Plugin must export a default definition with an id and an effect or setup function.");
  }
  const hooks = {};
  const registered = [];
  const subscriptions = [];
  const hookService = (svc) => ({
    hook(name, fn) {
      registered.push(svc + ":" + name);
      hooks[svc + ":" + name] = fn;
    },
  });
  const ctx = {
    location: { directory: cwd },
    tool: hookService("tool"),
    session: hookService("session"),
    event: {
      subscribe(opts) {
        registered.push("event:subscribe");
        const sub = { types: opts && opts.types, queue: [], wake: null };
        subscriptions.push(sub);
        return {
          async *[Symbol.asyncIterator]() {
            for (;;) {
              while (sub.queue.length > 0) {
                const item = sub.queue.shift();
                yield item.evt;
                item.done();
              }
              await new Promise((r) => (sub.wake = r));
            }
          },
        };
      },
    },
  };
  const returned = await def.setup(ctx);
  const call = (key) => (arg) => {
    if (typeof hooks[key] !== "function") throw new Error("no " + key + " hook registered");
    return hooks[key](arg);
  };
  return {
    returned,
    registered,
    subscriptions,
    before: call("tool:execute.before"),
    after: call("tool:execute.after"),
    compaction: call("session:compaction"),
    emit(evt) {
      const waits = [];
      for (const sub of subscriptions) {
        if (Array.isArray(sub.types) && !sub.types.includes(evt.type)) continue;
        waits.push(new Promise((done) => sub.queue.push({ evt, done })));
        if (sub.wake) {
          const w = sub.wake;
          sub.wake = null;
          w();
        }
      }
      return Promise.all(waits);
    },
  };
}

// toolArg is the one argument 2.2.7 hands execute.before.
function toolArg(tool, input) {
  return { tool, sessionID: "ses_test", agent: "build", messageID: "msg_test", id: "call_test", input };
}

// runToolCall drives one execute.before call and reports whether it threw.
async function runToolCall(host, arg) {
  try {
    await host.before(arg);
    return { threw: false };
  } catch (e) {
    return { threw: true, message: String(e && e.message ? e.message : e) };
  }
}
`

// hostDriver is a Node driver: it imports the plugin entry at NG_PLUGIN_PATH,
// loads it through fakeHostJS with NG_CWD as the worktree, binds it to
// `host`, then runs body.
func hostDriver(body string) string {
	return `import { pathToFileURL } from "node:url";
` + fakeHostJS + `
const mod = await import(pathToFileURL(process.env.NG_PLUGIN_PATH).href);
const host = await loadPlugin(mod.default, process.env.NG_CWD);
` + body
}

var nodeHarnessDriver = hostDriver(`
const arg = toolArg(process.env.NG_TOOL || "shell", { command: process.env.NG_COMMAND || "" });
process.stdout.write(JSON.stringify(await runToolCall(host, arg)));
`)

// TestMain removes the nightgauge binary buildNightgaugeBin builds into the
// OS temp directory (not a per-test t.TempDir(), since it is shared, via
// sync.Once, across every test in this package that needs it) once every
// test in the package has run.
func TestMain(m *testing.M) {
	// Tests here set NIGHTGAUGE_STATE_HOME for the CLI they spawn; HOME moves
	// with it so no spawned CLI pairs a temp STATE with the real HOME (#2311).
	cleanupHome := hometest.Isolate()
	// Real hook binaries spawned from os.Environ() must never post a desktop
	// banner during a gate run (#3137); TestRealHookBinariesDisableNotifications
	// pins this and the from-scratch env lists built with hookBinEnv.
	_ = os.Setenv(notifyDisableEnv, "1")
	code := m.Run()
	if nightgaugeBinPath != "" {
		_ = os.Remove(nightgaugeBinPath)
	}
	cleanupHome()
	os.Exit(code)
}

func requireNode(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("node is not on PATH, and CI must run this suite: %v", err)
		}
		t.Skip("node is not on PATH; install Node to run the plugin Node harness")
	}
	return bin
}

// nodeHarness drives one tool.execute.before call against the real embedded
// plugin. cwd is the directory the plugin is told is the worktree (and where
// the careful lock, if any, is read from); nightgaugeBin is exported as
// NIGHTGAUGE_BIN, "" leaving it unset.
type nodeHarnessResult struct {
	Threw   bool   `json:"threw"`
	Message string `json:"message"`
}

func runNodeHarness(t *testing.T, node, cwd, command, nightgaugeBin string, extraEnv map[string]string) nodeHarnessResult {
	t.Helper()
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	entry, err := Write(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(nodeHarnessDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driver)
	env := append(os.Environ(),
		"NG_PLUGIN_PATH="+filepath.Join(entry, EntryFile),
		"NG_CWD="+cwd,
		"NG_COMMAND="+command,
	)
	if nightgaugeBin != "" {
		env = withHookBin(env, nightgaugeBin)
	} else {
		env = removeEnv(env, "NIGHTGAUGE_BIN")
	}
	for k, v := range extraEnv {
		env = upsertEnv(env, k, v)
	}
	// An extraEnv map may set NIGHTGAUGE_BIN under a dynamic key the guard
	// cannot see, so the harness pairs it with the opt-out itself (#3250).
	env = upsertEnv(env, notifyDisableEnv, "1")
	cmd.Env = env
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node harness failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("node harness failed: %v", err)
	}
	var res nodeHarnessResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("node harness printed non-JSON: %s (%v)", out, err)
	}
	return res
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func removeEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

// nightgaugeBinOnce builds the real nightgauge binary once per test process,
// so the parity corpus test drives the actual `nightgauge hook careful-gate`
// verb rather than a stand-in.
var (
	nightgaugeBinOnce sync.Once
	nightgaugeBinPath string
	nightgaugeBinErr  error
)

func buildNightgaugeBin(t *testing.T) string {
	t.Helper()
	nightgaugeBinOnce.Do(func() {
		dir := os.TempDir()
		out := filepath.Join(dir, fmt.Sprintf("nightgauge-opencodeplugin-test-%d", os.Getpid()))
		cmd := exec.Command("go", "build", "-o", out, moduleRootRelative("cmd/nightgauge"))
		cmd.Dir = moduleRoot()
		if b, err := cmd.CombinedOutput(); err != nil {
			nightgaugeBinErr = fmt.Errorf("building nightgauge: %w\n%s", err, b)
			return
		}
		nightgaugeBinPath = out
	})
	if nightgaugeBinErr != nil {
		t.Fatalf("could not build the nightgauge binary for the node harness: %v", nightgaugeBinErr)
	}
	return nightgaugeBinPath
}

// moduleRoot walks up from this file's own package directory to the module
// root (the directory holding go.mod), so the build helper works regardless
// of the working directory `go test` was invoked from.
func moduleRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return wd
		}
		dir = parent
	}
}

func moduleRootRelative(pkg string) string {
	return "./" + pkg
}

// TestNodeHarnessGatesJS locks in the AC's Node-harness bullet: with a
// careful lock in the worktree, a real destructive command throws the
// [nightgauge-gate:careful] marker, and a safe one passes; NIGHTGAUGE_BIN
// unset, a spawn that times out, and a verb that exits non-zero each throw
// too.
func TestNodeHarnessGatesJS(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)

	t.Run("careful on blocks a destructive command", func(t *testing.T) {
		root := layouttest.Repo(t)
		if err := careful.Enable(root, 0, ""); err != nil {
			t.Fatal(err)
		}
		res := runNodeHarness(t, node, root, "docker compose down -v", bin, nil)
		if !res.Threw {
			t.Fatal("want a throw, got none")
		}
		if !strings.HasPrefix(res.Message, "[nightgauge-gate:careful]") {
			t.Errorf("message = %q, want the [nightgauge-gate:careful] marker", res.Message)
		}
	})

	t.Run("careful on allows a safe command", func(t *testing.T) {
		root := layouttest.Repo(t)
		if err := careful.Enable(root, 0, ""); err != nil {
			t.Fatal(err)
		}
		res := runNodeHarness(t, node, root, "docker compose up -d", bin, nil)
		if res.Threw {
			t.Fatalf("want no throw, got %q", res.Message)
		}
	})

	t.Run("NIGHTGAUGE_BIN unset blocks closed", func(t *testing.T) {
		root := t.TempDir()
		res := runNodeHarness(t, node, root, "echo hi", "", nil)
		if !res.Threw {
			t.Fatal("want a throw when NIGHTGAUGE_BIN is unset")
		}
	})

	t.Run("a relative NIGHTGAUGE_BIN blocks closed", func(t *testing.T) {
		root := t.TempDir()
		res := runNodeHarness(t, node, root, "echo hi", "relative/nightgauge", nil)
		if !res.Threw {
			t.Fatal("want a throw when NIGHTGAUGE_BIN is a relative path")
		}
	})

	t.Run("a hung verb times out and blocks closed", func(t *testing.T) {
		root := t.TempDir()
		// Must outlast gates.js's SPAWN_TIMEOUT_MS (15000ms); exec so the
		// timeout's SIGTERM reaches sleep itself and no child holds the pipes.
		sleeper := filepath.Join(t.TempDir(), "sleeper.sh")
		if err := os.WriteFile(sleeper, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		res := runNodeHarness(t, node, root, "echo hi", sleeper, nil)
		if !res.Threw {
			t.Fatal("want a throw when the gate verb hangs past its timeout")
		}
	})

	t.Run("a verb that exits non-zero blocks closed", func(t *testing.T) {
		root := t.TempDir()
		exit3 := filepath.Join(t.TempDir(), "exit3.sh")
		if err := os.WriteFile(exit3, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		res := runNodeHarness(t, node, root, "echo hi", exit3, nil)
		if !res.Threw {
			t.Fatal("want a throw when the gate verb exits non-zero")
		}
	})

	t.Run("output that does not parse blocks closed", func(t *testing.T) {
		root := t.TempDir()
		garbage := filepath.Join(t.TempDir(), "garbage.sh")
		if err := os.WriteFile(garbage, []byte("#!/bin/sh\necho 'not json'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		res := runNodeHarness(t, node, root, "echo hi", garbage, nil)
		if !res.Threw {
			t.Fatal("want a throw when the gate verb's output does not parse")
		}
	})
}

// TestNodeHarnessDeniesSubagent: the `subagent` tool (1.x's `task`) is
// always denied, careful mode on or off, and independent of NIGHTGAUGE_BIN or
// any gate verb (no verb is spawned for it). Weakening gates.js's subagent
// check to a no-op turns this red.
func TestNodeHarnessDeniesSubagent(t *testing.T) {
	node := requireNode(t)

	t.Run("subagent is denied with no NIGHTGAUGE_BIN and careful off", func(t *testing.T) {
		root := t.TempDir()
		res := runNodeHarness(t, node, root, "", "", map[string]string{"NG_TOOL": "subagent"})
		if !res.Threw {
			t.Fatal("want a throw for the subagent tool")
		}
		if !strings.HasPrefix(res.Message, "[nightgauge-gate:subagent-denied]") {
			t.Errorf("message = %q, want the [nightgauge-gate:subagent-denied] marker", res.Message)
		}
	})

	t.Run("subagent is denied with careful on", func(t *testing.T) {
		root := layouttest.Repo(t)
		bin := buildNightgaugeBin(t)
		if err := careful.Enable(root, 0, ""); err != nil {
			t.Fatal(err)
		}
		res := runNodeHarness(t, node, root, "", bin, map[string]string{"NG_TOOL": "subagent"})
		if !res.Threw {
			t.Fatal("want a throw for the subagent tool")
		}
		if !strings.HasPrefix(res.Message, "[nightgauge-gate:subagent-denied]") {
			t.Errorf("message = %q, want the [nightgauge-gate:subagent-denied] marker", res.Message)
		}
	})

	t.Run("shell is unaffected", func(t *testing.T) {
		root := t.TempDir()
		bin := buildNightgaugeBin(t)
		res := runNodeHarness(t, node, root, "echo hi", bin, map[string]string{"NG_TOOL": "shell"})
		if res.Threw {
			t.Fatalf("want no throw for shell with a safe command, got %q", res.Message)
		}
	})

	t.Run("1.x's task is refused as unknown", func(t *testing.T) {
		root := t.TempDir()
		res := runNodeHarness(t, node, root, "", "", map[string]string{"NG_TOOL": "task"})
		if !res.Threw || !strings.HasPrefix(res.Message, "[nightgauge-gate:unknown-tool]") {
			t.Errorf("task: threw=%v message=%q, want the [nightgauge-gate:unknown-tool] marker", res.Threw, res.Message)
		}
	})
}

// TestNodeHarnessDeniesSubagentRegardlessOfCostBudget pins AC1 of #1748: a
// stage dispatched with a CostBudget still has its `subagent` calls denied, so
// no subagent session can start to spend past that budget. CostBudget is
// consumed on the Go side (openCodeCostWatchdog) and never reaches the plugin;
// this pins the unconditional deny against a future change that threads a
// cost-budget signal into the plugin without preserving it.
func TestNodeHarnessDeniesSubagentRegardlessOfCostBudget(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	res := runNodeHarness(t, node, root, "", "", map[string]string{
		"NG_TOOL":                "subagent",
		"NIGHTGAUGE_COST_BUDGET": "0.01",
	})
	if !res.Threw {
		t.Fatal("want a throw for the subagent tool even with a cost budget present")
	}
	if !strings.HasPrefix(res.Message, "[nightgauge-gate:subagent-denied]") {
		t.Errorf("message = %q, want the [nightgauge-gate:subagent-denied] marker", res.Message)
	}
}

// setupDriver loads the plugin through the fake 2.x host and reports what
// setup registered, what it returned, and the sentinel it wrote.
var setupDriver = hostDriver(`
process.stdout.write(JSON.stringify({
  registered: host.registered,
  returnedType: typeof host.returned,
  subscribedTypes: host.subscriptions.map((s) => s.types || null),
}));
`)

// TestSetupRegistersThroughCtxAndReturnsNothing (#3302 AC 1): the entry's
// default export is the 2.x {id, setup} definition, setup registers the gates
// through ctx.tool.hook("execute.before"/"execute.after") in HookNames order,
// and returns undefined: a returned 1.x hook map registers nothing on 2.x. The
// sentinel it writes names the same registrations. Reverting nightgauge.js to
// a returned hook map turns this red.
func TestSetupRegistersThroughCtxAndReturnsNothing(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	dir, err := Write(filepath.Join(t.TempDir(), "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(setupDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel.json")
	cmd := exec.Command(node, driver)
	cmd.Env = append(removeEnv(os.Environ(), "NIGHTGAUGE_BIN"),
		"NG_PLUGIN_PATH="+filepath.Join(dir, EntryFile),
		"NG_CWD="+root,
		EnvNonce+"=n-3302",
		EnvSentinel+"="+sentinel,
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("setup driver: %v (%s)", err, out)
	}
	var got struct {
		Registered      []string   `json:"registered"`
		ReturnedType    string     `json:"returnedType"`
		SubscribedTypes [][]string `json:"subscribedTypes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("driver output %s: %v", out, err)
	}
	if got.ReturnedType != "undefined" {
		t.Errorf("setup returned a %s, want undefined: OpenCode 2.x ignores a returned hook map", got.ReturnedType)
	}
	if strings.Join(got.Registered, ",") != strings.Join(HookNames, ",") {
		t.Errorf("setup registered %v, want HookNames %v in order", got.Registered, HookNames)
	}
	if len(got.SubscribedTypes) != 1 {
		t.Fatalf("setup subscribed %d times (types %v), want one subscription", len(got.SubscribedTypes), got.SubscribedTypes)
	}
	// Every type session.js's event() handles must pass the filter, or a real
	// run never delivers it.
	for _, want := range []string{"session.created", "session.compaction.started", "permission.asked", "session.execution.succeeded", "session.execution.failed", "session.text.delta"} {
		if !strings.Contains(","+strings.Join(got.SubscribedTypes[0], ",")+",", ","+want+",") {
			t.Errorf("subscription types %v lack %q", got.SubscribedTypes[0], want)
		}
	}
	var s Sentinel
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("setup wrote no sentinel: %v", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if s.Nonce != "n-3302" || s.PluginVersion != PluginVersion || strings.Join(s.Hooks, ",") != strings.Join(HookNames, ",") {
		t.Errorf("sentinel = %+v, want nonce n-3302, version %s, hooks %v", s, PluginVersion, HookNames)
	}
}

// carefulParityRow is one row of testdata/careful_parity_corpus.json.
type carefulParityRow struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Careful bool   `json:"careful"`
	Expect  string `json:"expect"` // "allow" or "block"
}

// TestCarefulGateParity compares, row by row, the verdict
// hooks.EvaluateCarefulGate gives directly against the verdict the plugin's
// gates.js gives by shelling out to the real, built `nightgauge hook
// careful-gate` verb — the same verb the Claude Code hook runs. Because
// gates.js delegates to that verb rather than re-implementing the careful
// gate's pattern matching in JS, parity holds by construction; this test
// guards the delegation itself (the stdin envelope, cwd, and the
// allow/deny decoding of printPreToolUse's output).
func TestCarefulGateParity(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)

	data, err := os.ReadFile(filepath.Join("testdata", "careful_parity_corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []carefulParityRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("the parity corpus is empty")
	}

	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			root := layouttest.Repo(t)
			if row.Careful {
				if err := careful.Enable(root, 0, ""); err != nil {
					t.Fatal(err)
				}
			}

			// The Go side, direct: what internal/hooks.EvaluateCarefulGate
			// itself decides — the corpus's own claim about the source of
			// truth.
			input, err := json.Marshal(map[string]any{
				"tool_name": "Bash",
				"cwd":       root,
				"tool_input": map[string]any{
					"command": row.Command,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			goDecision := hooks.EvaluateCarefulGate(input)
			wantGo := "allow"
			if goDecision.Decision == "block" {
				wantGo = "block"
			}
			if wantGo != row.Expect {
				t.Fatalf("corpus row %q claims %q, but hooks.EvaluateCarefulGate itself says %q — the corpus is stale", row.Name, row.Expect, wantGo)
			}

			// The plugin side: gates.js, via the real built binary.
			res := runNodeHarness(t, node, root, row.Command, bin, nil)
			gotPlugin := "allow"
			if res.Threw {
				gotPlugin = "block"
			}
			if gotPlugin != row.Expect {
				t.Errorf("plugin verdict = %q, want %q (message: %q)", gotPlugin, row.Expect, res.Message)
			}
		})
	}
}

// notifyDisableEnv mirrors hooks.NotifyDisableEnv for the spawned binaries.
const notifyDisableEnv = "NIGHTGAUGE_NOTIFY_DISABLE"

// hookBinEnv is the only place a test names the hook binary: it pairs
// NIGHTGAUGE_BIN with the notification opt-out so a from-scratch env list
// cannot spawn a real build that posts a banner (#3137).
func hookBinEnv(bin string) []string {
	return []string{"NIGHTGAUGE_BIN=" + bin, notifyDisableEnv + "=1"}
}

// withHookBin sets the hook binary on an existing env list, replacing any
// NIGHTGAUGE_BIN and opt-out already there, through hookBinEnv (#3250).
func withHookBin(env []string, bin string) []string {
	env = removeEnv(env, "NIGHTGAUGE_BIN")
	env = removeEnv(env, notifyDisableEnv)
	return append(env, hookBinEnv(bin)...)
}
