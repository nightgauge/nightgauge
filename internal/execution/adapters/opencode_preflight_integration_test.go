//go:build opencode_integration

package adapters

// Built with the opencode_integration tag, the package's tests run the real
// opencode binary, so TestMain leaves PATH as it is.
func init() { openCodeIntegrationBuild = true }
