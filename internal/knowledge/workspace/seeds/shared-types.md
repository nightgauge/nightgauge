# Shared Types

Document the package or schema that owns wire types shared across repositories.

## Source of truth

- Package or schema:
- Publishing location:
- Consumers:
- Compatibility policy:

## Update flow

1. Change the authoritative schema.
2. Run validation and generate dependent clients or types.
3. Publish or vendor the versioned artifact.
4. Upgrade consumers and run contract tests.

Avoid hand-written duplicate response types. Coordinate removals or required
field changes across every consumer before publishing an incompatible version.
