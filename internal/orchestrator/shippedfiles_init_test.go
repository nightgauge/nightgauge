package orchestrator

import "github.com/nightgauge/nightgauge/internal/shippedfiles/shippedtest"

// The OpenCode plugin and pre-push hook ship beside the binary, not inside
// it (#3383); a test binary finds them in the checkout.
func init() { shippedtest.UseCheckout() }
