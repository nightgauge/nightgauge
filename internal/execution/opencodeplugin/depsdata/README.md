# `opencode-ai-plugin-<version>.tar.gz` — provenance

This archive is embedded into the Nightgauge binary
(`internal/execution/opencodeplugin/deps.go`) and extracted, never
installed, into the RUN'S OWN OpenCode config directory only, so opencode
1.18.30's own `@opencode-ai/plugin` install never runs there — see the doc
comment on `WriteDependencies` for what it does with the archive.

Nightgauge never seeds or merges anything into an operator-owned OpenCode
config directory (`$HOME/.opencode`, an inherited `OPENCODE_CONFIG_DIR`) —
see "Operator-owned directories" below.

## What the archive holds, and why

opencode 1.18.30 installs the npm package `@opencode-ai/plugin` into any
OpenCode config directory whose resolved config carries a non-empty `plugin`
array, independent of whether a plugin file actually imports that package,
and every invocation that resolves such a config waits for that install
before doing anything else. Driving the real, pinned 1.18.30 binary against
progressively smaller trees (`opencode debug config`, offline, timed) — the
#1635 fix round 2 methodology, repeated here for anyone re-verifying it —
established that opencode's own "is this already installed" check reads
exactly four files:

| File                                            | Role                                                                                         |
| ----------------------------------------------- | -------------------------------------------------------------------------------------------- |
| `package.json`                                  | the resolved dependency declaration (`{"dependencies":{"@opencode-ai/plugin":"<version>"}}`) |
| `package-lock.json`                             | the root npm lockfile                                                                        |
| `node_modules/.package-lock.json`               | npm's own hidden lockfile                                                                    |
| `node_modules/@opencode-ai/plugin/package.json` | the version marker opencode compares against `package.json`                                  |

The archive holds exactly those four files — nothing else, not even
`node_modules/@opencode-ai/plugin/dist/`. Removing any one of the four and
re-running `opencode debug config` offline makes it hang for the length of
npm's registry retry/backoff window (observed tens of seconds against a
closed port; ADR-022 amendment 2026-09-14 records the ~71s and ~146.88s
waits an unseeded directory produced in production). That trim is safe
**only** for a run's own, freshly-created OpenCode config directory — where
the only thing that ever loads is the embedded Nightgauge plugin
(`../plugin/nightgauge.js`, `../plugin/nightgauge/gates.js`), and neither
file ever imports `@opencode-ai/plugin` or anything in its dependency tree
at runtime; both import only `node:*` built-ins and each other.

## Operator-owned directories: never seeded, never merged into

A #1635/A11 round 5 review found the four-file trim above does not extend to
an **operator-owned** directory (`$HOME/.opencode`, an inherited
`OPENCODE_CONFIG_DIR`): those may hold the operator's own tools or plugins,
and OpenCode's own documented way to write a custom tool is
`import { tool } from "@opencode-ai/plugin"`. Seeding such a directory with
the four-file stub satisfies opencode's own install check _forever_ — it
never re-installs once satisfied — while leaving that import permanently
unresolvable, since no `dist/` ever arrives to satisfy it.

Round 5's fix re-embedded the complete, real installed `@opencode-ai/plugin`
tree (`dist/` included, ~10.4 MB gzipped, 3,618 files) as a second archive
and merged it into an operator directory non-destructively. A #1635/A11
round 6 review found that fix itself brittle (a `null` `"dependencies"` key
panicked the merge; an interrupted merge could leave the version marker
written with no `dist/` beside it) and, independent of that, a poor trade for
a public repository: ~10.4 MB of opaque, re-embedded third-party code for a
directory Nightgauge does not own and should not be modifying at all.

**Round 6's decision (ADR-022 amendment 2026-09-15, narrowed AC1): Nightgauge
never writes into an operator-owned OpenCode directory, full stop.** The
second archive, `MergeDependencies`, and every merge/seed call site for
`$HOME/.opencode` and an inherited `OPENCODE_CONFIG_DIR` are removed.
OpenCode's own install into its own config directories is the operator's
environment, exactly as in the operator's own OpenCode runs — no different
from what would happen if the operator ran `opencode` themselves. Offline, or
against an unreachable registry, that install can still wait; narrowed AC1
requires that wait to be bounded by the stage's own context and to fail the
dispatch with a clear, classified (`adapter_incompatible`) error rather than
hang (`internal/execution/manager.go`'s operator-install-risk watchdog).

`opencodeplugin.OperatorInstallSatisfied` is a READ-ONLY check (never a
write) of whether a directory holds the FULL set opencode 1.18.30's own "is
`@opencode-ai/plugin` already installed" check reads (the table above), not
only the version marker. Round 7 measured only the marker and, finding a
marker-only directory still paid the ~70-80s registry round trip, concluded
no operator directory ever gets a fast path and dropped the exemption for a
satisfied one entirely. **Round 8 (ADR-022 amendment 2026-09-15) corrects
this:** driven against the real binary with the full four-file set actually
seeded, an operator directory that satisfies opencode's own check DOES get
the same local, instant fast path a run's own XDG-resolved config directory
always did. `adapters.operatorInstallRisk` arms `manager.go`'s
operator-install-risk watchdog only for a directory in play that
`OperatorInstallSatisfied` reports unsatisfied; once armed, the watchdog
stands down on either the directory becoming satisfied (polled, read-only)
or the first output, so it never caps model latency once OpenCode's own
install completes. `nightgauge/nightgauge#1787` (a per-run `HOME`, so
`$HOME/.opencode` stops being a config directory at all) is the tracked path
to removing the wait entirely, rather than only bounding it.

## How the archive is built

`regenerate/main.go` (`go run ./internal/execution/opencodeplugin/depsdata/regenerate --version <version>`):

1. Runs a real `npm install --omit=optional --no-audit --no-fund
--ignore-scripts` of exactly `@opencode-ai/plugin@<version>` in a scratch
   directory, so `package-lock.json` and `node_modules/.package-lock.json`
   are npm's own authentic output, not hand-written.
2. Builds the archive from exactly the four files in the table above; the
   root `package.json` is replaced with the canonical minimal form
   regardless of what the scratch install's own `package.json` said (npm
   needs a `"name"` to install into a directory cleanly; opencode's own check
   does not read one).
3. Tars and gzips it with a fixed modification time, zeroed owner, and
   sorted entry order, so two runs against the same npm registry state
   produce byte-identical archives — verified by running the program twice
   and diffing the SHA-256 sums of both outputs.

`--ignore-scripts` disables npm lifecycle scripts for every package in the
tree, including transitive ones, during regeneration itself. No lifecycle
script, npm binary, or network request ever runs when Nightgauge extracts
the archive at dispatch time — only at regeneration time, by a maintainer,
never by a test or by CI.

## Regenerating for a new opencode version

```
go run ./internal/execution/opencodeplugin/depsdata/regenerate --version <new version>
```

Then update `opencodeplugin.DepsVersion` in `deps.go` to match —
`TestDepsArchiveMatchesPinnedVersion` fails until both agree — and re-run the
real-binary integration suite
(`go test -tags opencode_integration ./internal/execution/... ./internal/execution/opencodeplugin/...`)
to reconfirm the four-file set is still everything the new version's install
check reads: opencode's own check is an implementation detail this
repository does not control, and a future version could read one more file
than 1.18.30 does.

## Size budget

`TestDepsArchiveSizeBudget` (`deps_test.go`) fails if the archive exceeds
**32 KiB** compressed. The four pinned files run about 26 KB uncompressed
(package-lock.json and node_modules/.package-lock.json dominate, at roughly
14 KB and 9.5 KB — real npm lockfile output for `@opencode-ai/plugin`'s full
dependency closure, kept in full) and gzip to under 5 KB as of 1.18.30. 32
KiB leaves over 6x headroom for a future `@opencode-ai/plugin` version whose
dependency closure grows before anyone revisits the budget, while still
catching a regression back toward embedding a whole installed tree by orders
of magnitude before it reaches even 1% of the original ~11 MB round 1
shipped.
