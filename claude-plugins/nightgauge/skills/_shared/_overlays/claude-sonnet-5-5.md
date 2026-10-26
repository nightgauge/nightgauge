Claude Sonnet 5.5. This countermands parts of the guidance above.

- The stage's own gates are the verification. Do not add an extra
  verify-your-work pass on top of them, and ignore any earlier line
  asking for one.
- Keep PR, issue and retro bodies tight. Do not narrate each tool
  call: one opening line, one closing recap.
- Ignore the "every step explicitly" guidance above where a step's
  intent is already satisfied in this run: note that in a line and
  move on. Still run every gate and write the context file.
