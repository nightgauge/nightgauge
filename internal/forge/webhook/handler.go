package webhook

import "net/http"

// Handler is implemented by platform-specific webhook handlers (e.g. GitLab).
// Register mounts the platform's routes onto mux at the given basePath.
type Handler interface {
	Register(mux *http.ServeMux, basePath string)
}

// NewMux mounts handler.Register onto a fresh ServeMux and appends the
// canonical /-/health and /-/metrics probes. It returns the resulting
// http.Handler for use by Server.
func NewMux(handler Handler, basePath string) http.Handler {
	mux := http.NewServeMux()
	handler.Register(mux, basePath)
	return mux
}
