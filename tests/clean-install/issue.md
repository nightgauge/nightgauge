## Summary

`src/index.js` exports `slugify` only. Add a second, independent function
`truncate(text, maxLength)` and export it alongside `slugify`:

- Returns `text` unchanged when `text.length <= maxLength`.
- Otherwise returns the first `maxLength - 1` characters of `text` followed by
  a single `…` (U+2026), so the result is never longer than `maxLength`.
- Throws `TypeError` when `text` is not a string.
- Throws `RangeError` when `maxLength` is not an integer `>= 1`.

No new dependencies. Keep the CommonJS `module.exports = { slugify, truncate }`
shape and the existing `node --test` runner.

## Verification

1. `test/truncate.test.js` covers: unchanged when short enough, truncated with
   `…` when too long, the boundary (`text.length === maxLength` is unchanged),
   the `TypeError` and the `RangeError`.
2. `npm test` passes with the existing `slugify` tests untouched.
3. `README.md` lists `truncate` next to `slugify`.

## Security constraints

- Pure function; no I/O, no `eval`, no new dependencies.
- Do not change `slugify`.
