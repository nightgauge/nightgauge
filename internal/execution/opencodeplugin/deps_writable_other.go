//go:build !unix

package opencodeplugin

// dirUnwritable always reports false on a platform this package's unix
// access(2) check does not cover — Nightgauge's own build targets are
// darwin and linux only (the repository Makefile builds no other GOOS).
// OperatorInstallSatisfied's own node_modules/package.json checks still
// decide the answer for a directory this cannot classify.
func dirUnwritable(dir string) bool { return false }
