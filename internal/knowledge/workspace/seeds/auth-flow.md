# Authentication Flow

Document authentication only when it is part of the public architecture of the
workspace.

## Actors and credentials

| Actor               | Credential type | Storage | Scope | Rotation owner |
| ------------------- | --------------- | ------- | ----- | -------------- |
| Interactive user    |                 |         |       |                |
| Automation          |                 |         |       |                |
| Service integration |                 |         |       |                |

## Invariants

- Never store raw passwords or tokens in repository files.
- Use the operating system, CI provider, or secret manager for credential
  storage.
- Document public request/response contracts, not private signing material or
  infrastructure topology.
- Record expiration, revocation, and least-privilege behavior.

Link to the authoritative public security documentation instead of duplicating
it here.
