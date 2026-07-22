# Reference: Configuration Keys & Environment Overrides

Full config schema consulted across the validation phases. Read this when you
need a config key default or an environment-override name.

This skill reads configuration from `.nightgauge/config.yaml`. See
[docs/CONFIGURATION.md](../../../docs/CONFIGURATION.md) for the full schema
reference.

| Config Key                         | Default    | Description                                                       |
| ---------------------------------- | ---------- | ----------------------------------------------------------------- |
| `commands.build`                   | auto       | Build command override                                            |
| `commands.test`                    | auto       | Test command override                                             |
| `pipeline.skip.tests`              | `false`    | Skip test execution                                               |
| `pipeline.skip.build`              | `false`    | **NOT RECOMMENDED** - Skip build check                            |
| `project.number`                   | -          | GitHub Project number                                             |
| `ralph_loop.enabled`               | `true`     | Enable Ralph Loop self-healing                                    |
| `ralph_loop.build`                 | `true`     | Enable Ralph Loop for build phase                                 |
| `ralph_loop.tests`                 | `true`     | Enable Ralph Loop for tests phase                                 |
| `ralph_loop.limits.max_iterations` | `3`        | Max fix attempts per error                                        |
| `pipeline.targeted_tests`          | `"auto"`   | Targeted test selection: "auto", "always", "never"                |
| `validation.dead_code`             | `"gate"`   | Dead code gating: "gate", "warn", "off"                           |
| `validation.integration_check`     | `"warn"`   | Integration check: "warn", "gate", "off"                          |
| `validation.integration_tests`     | `"strict"` | Integration **test** gate (#2909): "strict", "best_effort", "off" |

Environment overrides: `NIGHTGAUGE_COMMANDS_BUILD`,
`NIGHTGAUGE_COMMANDS_TEST`, `NIGHTGAUGE_PIPELINE_SKIP_TESTS`,
`NIGHTGAUGE_PIPELINE_TARGETED_TESTS`,
`NIGHTGAUGE_VALIDATION_DEAD_CODE`,
`NIGHTGAUGE_VALIDATION_INTEGRATION_CHECK`.
