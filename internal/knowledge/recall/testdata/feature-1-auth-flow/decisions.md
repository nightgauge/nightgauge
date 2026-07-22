---
tags: [authentication, oauth, security]
status: stable
---

# Decisions: #1 — Auth Flow

## ADR-001: OAuth2 PKCE for Authentication

**Status**: Stable
**Context**: The platform requires secure user authentication without storing client secrets in the mobile app. Several options were evaluated including traditional OAuth2 with client secret, API key auth, and PKCE.
**Decision**: Use OAuth2 PKCE flow for all mobile authentication. PKCE provides security without requiring a client secret in the mobile app bundle. The authorization server validates the code challenge on every token exchange.
**Consequences**: All authentication flows must implement PKCE. Token storage uses the platform keychain. Refresh tokens are rotated on every use to limit the blast radius of a stolen token.
