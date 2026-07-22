# Flow: dashboard-auth

Reference flow — sign in to `acme-dashboard` and confirm an
authenticated route renders. Copy this file as the template for new flows.

- **Default base URL:** `http://localhost:5173` (Vite dev) — override with `--url`.
- **Credentials:** read from env `IB_VERIFY_EMAIL` / `IB_VERIFY_PASSWORD` (never
  hardcode secrets in the flow). Skip the flow with a clear message if unset.

> Selectors below are written against the standard email/password login. Confirm
> them against the live app on first run (`browser_snapshot` shows the actual
> accessibility tree) and adjust — prefer role/text/test-id over CSS.

## Steps

| #   | Action                                               | Assertion (hard-fail on mismatch)                                            |
| --- | ---------------------------------------------------- | ---------------------------------------------------------------------------- |
| 1   | `browser_navigate` → `${BASE_URL}/login`             | Snapshot shows the login form (role `textbox` "Email", `textbox` "Password") |
| 2   | `browser_type` email field ← `IB_VERIFY_EMAIL`       | Email field value reflects the input                                         |
| 3   | `browser_type` password field ← `IB_VERIFY_PASSWORD` | Password field is populated (masked)                                         |
| 4   | `browser_click` the "Sign in" / submit button        | A POST to the auth endpoint returns 2xx (`browser_network_requests`)         |
| 5   | `browser_wait_for` navigation away from `/login`     | URL no longer matches `/login`; an authenticated landmark is visible         |
| 6   | `browser_snapshot` the authenticated shell           | A signed-in marker (user menu / sign-out control) is present                 |

## Pass criteria

All six steps pass. Any 4xx/5xx on step 4, or remaining on `/login` after step 5,
is a **failure** (a flow that ends back on the login page did not authenticate).

## Notes

- For other repos, create `flows/<name>.md` with the same shape: ordered
  action+assertion rows, a default URL, env-sourced inputs, and explicit pass
  criteria. Good candidates: a acme-web variation-generation flow, an acme-site
  contact-form submit, a flutter-web smoke route.
