# Add CI build verification to platform repo

## Summary

Add a CI workflow that verifies the platform builds and tests pass on every
PR to `main`. Existing internal docs cover the deployment runbook but do not
cover CI gating policy.

## Acceptance Criteria

- [x] New file `.github/workflows/ci.yml` exists
- [x] Workflow `.github/workflows/ci.yml` has job `build`
- [x] `npm run lint` script exists in `package.json`
- [x] `npm run test` script exists in `package.json`
- [x] Documented in `docs/CI_INTEGRATION.md` section `Required Checks`
- [ ] Branch protection on `main` requires required check `build`

## Notes

This issue captures the captured snapshot used by Issue #3003's integration
test. Six ACs total — five satisfied via local fixtures, one undetectable
(branch protection requires `gh` auth).
