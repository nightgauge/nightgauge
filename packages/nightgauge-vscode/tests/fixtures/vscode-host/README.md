# VSCode host smoke tier fixtures

`populated/` is copied into the throwaway workspace folder partway through a
host run, by `tests/vscode-host/fixture.ts`.

There is no `empty/` directory: "empty" is the workspace folder as the
launcher creates it — a fresh temp directory with nothing in it. That is not
an oversight. The extension's `activationEvents` are
`workspaceContains:.nightgauge/pipeline` and
`workspaceContains:.nightgauge/plans`, so a committed "empty" fixture that
carried even a `.gitkeep` under `.nightgauge/` would auto-activate the
extension on window open. Activation would then race the loading of the test
module, and any rejection it threw would be gone before the observation layer
existed to see it — which is the single thing this tier is here to catch.

Nothing in `populated/` is asserted on. The tier asks whether each surface
comes up against a workspace with content; whether it comes up with the
_right_ content is the data-arrival tier (#746).
