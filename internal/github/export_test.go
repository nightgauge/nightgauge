package github

import "net/http/httptest"

// NewCIServiceForTest exposes newCIServiceForRESTTest to this directory's
// external test package, which needs a real *CIService pointed at a fake
// server to drive internal/hooks through it.
func NewCIServiceForTest(server *httptest.Server) *CIService {
	return newCIServiceForRESTTest(server)
}
