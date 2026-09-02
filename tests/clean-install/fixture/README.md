# clean-install-fixture

A deliberately tiny string-utilities package. It exists only to seed the
throwaway repository used by Nightgauge's clean-install release gate
(`scripts/clean-install-e2e.sh`), so that a freshly installed extension has
one real, small issue to drive to a merged pull request.

```bash
npm test
```

`src/index.js` exports `slugify`. The gate's single issue asks for a second,
missing function; see `tests/clean-install/issue.md` in the Nightgauge
repository.
