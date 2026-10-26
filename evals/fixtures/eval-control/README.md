# Eval control fixture seed

Seed for the pinned **control** task (#2117). `setup.sh` writes a small
TypeScript ledger package (`src/ledger.ts`, green `tests/ledger.test.ts`) and a
GitHub Actions workflow (`npm install`, `npm run typecheck`, `npm test`).

The control runs against the public repository `nightgauge/eval-control-fixture`,
whose first commit is this seed. That commit is the control's frozen
`base_sha` and is **never moved**: the task's content hash covers it, and runs
across model generations are only comparable while it stays fixed.

## Seeding the repository (one time)

```bash
tmp=$(mktemp -d) && cd "$tmp"
bash <repo>/evals/fixtures/eval-control/setup.sh
git init -b main && git add -A && git commit -m "eval control fixture base"
gh repo create nightgauge/eval-control-fixture --public
git remote add origin https://github.com/nightgauge/eval-control-fixture.git
git push -u origin main
git rev-parse HEAD   # the base_sha
```

Then open the control issue on that repository and register it as described in
[`evals/tasks/README.md`](../../tasks/README.md#control-task).

## Changing the seed

Any change here (for example adding pipeline config) is a **new** control: push
a new commit, then re-register `base_sha` and `content_sha256`. Never rewrite
the existing commit or edit the control issue.
