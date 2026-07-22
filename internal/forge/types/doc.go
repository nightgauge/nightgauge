// Package forgetypes contains forge-agnostic data types used by the
// internal/forge interfaces and adapters.
//
// Types defined here are pure data — no GitHub-, GitLab-, or HTTP-specific
// fields leak through. Adapters (e.g. internal/github) translate forge
// requests to and from concrete API responses; callers consume only the
// types in this package.
//
// Many types in this package are aliases of types in the public
// pkg/types package, which predates the forge abstraction. Aliasing
// preserves backwards compatibility for the ~27 existing import sites
// without forcing a coordinated migration. New consumers should import
// types from this package.
package forgetypes
