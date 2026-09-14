package models

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixtures in testdata/local-discovery are real responses captured from
// LM Studio and Ollama servers on loopback (README.md there).
const (
	lmStudioListingFixture  = "testdata/local-discovery/lmstudio-api-v0-models.json"
	ollamaNumCtxFixture     = "testdata/local-discovery/ollama-api-show-num-ctx.json"
	ollamaNoNumCtxFixture   = "testdata/local-discovery/ollama-api-show-no-num-ctx.json"
	capturedLMStudioModel   = "qwen/qwen3.8-27b"
	capturedNotLoadedModel  = "text-embedding-nomic-embed-text-v1.5"
	capturedOllamaNumCtx    = "qwen3-ctx32k"
	capturedOllamaNoNumCtx  = "qwen3:0.6b"
	capturedLoadedContext   = 131072
	capturedMaxContext      = 262144
	capturedOllamaNumCtxVal = 32768
)

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// resetLocalCache empties the process's discoveries before and after the
// test, so a server started on a port an earlier test's server used is never
// answered from that server's result.
func resetLocalCache(t *testing.T) {
	t.Helper()
	clear := func() {
		localCache.Range(func(k, _ any) bool {
			localCache.Delete(k)
			return true
		})
	}
	clear()
	t.Cleanup(clear)
}

// localServer is a stub model server that records every request it gets.
type localServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
}

func newLocalServer(t *testing.T, handler http.HandlerFunc) *localServer {
	t.Helper()
	s := &localServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Clone(r.Context()))
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *localServer) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// lmStudioServer serves body as LM Studio's GET /api/v0/models.
func lmStudioServer(t *testing.T, body []byte) *localServer {
	t.Helper()
	return newLocalServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// ollamaServer serves each model's body as Ollama's POST /api/show, and
// Ollama's 404 for any other model.
func ollamaServer(t *testing.T, shows map[string][]byte) *localServer {
	t.Helper()
	return newLocalServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/show" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		body, ok := shows[req.Model]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"model '` + req.Model + `' not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// withLoadedContext is the captured LM Studio listing with the captured
// model loaded with context tokens.
func withLoadedContext(t *testing.T, context int) []byte {
	t.Helper()
	var listing map[string]any
	if err := json.Unmarshal(readFixture(t, lmStudioListingFixture), &listing); err != nil {
		t.Fatal(err)
	}
	for _, m := range listing["data"].([]any) {
		entry := m.(map[string]any)
		if entry["id"] == capturedLMStudioModel {
			entry["loaded_context_length"] = context
		}
	}
	out, err := json.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertUnresolved fails unless err is a reason naming want, holding neither
// the address of srv nor a descriptor.
func assertUnresolved(t *testing.T, desc LocalDescriptor, err error, srvURL string, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("resolved %+v; want unresolved", desc)
	}
	if desc != (LocalDescriptor{}) {
		t.Errorf("an unresolved result carries a descriptor: %+v", desc)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the reason does not say %q: %v", w, err)
		}
	}
	if srvURL != "" {
		host := strings.TrimPrefix(srvURL, "http://")
		if strings.Contains(err.Error(), host) || strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("the reason names the endpoint's address: %v", err)
		}
	}
}

// TestResolveLocalLMStudioReadsLoadedContext: the window is the context LM
// Studio has loaded the model with, not the most it could load it with.
func TestResolveLocalLMStudioReadsLoadedContext(t *testing.T) {
	resetLocalCache(t)
	srv := lmStudioServer(t, readFixture(t, lmStudioListingFixture))
	t.Setenv(LMStudioBaseURLEnv, srv.URL+"/v1")

	desc, err := ResolveLocal("opencode", "lmstudio/"+capturedLMStudioModel)
	if err != nil {
		t.Fatalf("ResolveLocal: %v", err)
	}
	if desc.ContextWindow == capturedMaxContext {
		t.Fatalf("context_window is max_context_length (%d); it must be loaded_context_length", capturedMaxContext)
	}
	want := LocalDescriptor{
		Endpoint:      "lmstudio",
		Provider:      "lm-studio",
		Model:         capturedLMStudioModel,
		ContextWindow: capturedLoadedContext,
		MaxOutput:     0,
		ToolCall:      true,
		Reasoning:     nil,
	}
	if desc != want {
		t.Errorf("descriptor = %+v; want %+v", desc, want)
	}
	if srv.hits() != 1 || srv.requests[0].URL.Path != "/api/v0/models" {
		t.Errorf("the server got %d request(s); want one GET /api/v0/models", srv.hits())
	}
}

// TestResolveLocalLMStudioUnresolved: a listed model LM Studio has not
// loaded, and one it does not list, are unresolved with a reason naming the
// model, so a caller can fail closed.
func TestResolveLocalLMStudioUnresolved(t *testing.T) {
	resetLocalCache(t)
	srv := lmStudioServer(t, readFixture(t, lmStudioListingFixture))
	t.Setenv(LMStudioBaseURLEnv, srv.URL+"/v1")

	desc, err := ResolveLocal("opencode", "lmstudio/"+capturedNotLoadedModel)
	assertUnresolved(t, desc, err, srv.URL, capturedNotLoadedModel, `"not-loaded"`, "endpoint lmstudio")

	desc, err = ResolveLocal("opencode", "lmstudio/qwen/not-downloaded")
	assertUnresolved(t, desc, err, srv.URL, "qwen/not-downloaded", "does not list")

	noContext := bytes.Replace(readFixture(t, lmStudioListingFixture), []byte(`"loaded_context_length": 131072,`), nil, 1)
	other := lmStudioServer(t, noContext)
	desc, err = ResolveLocal("opencode", "lmstudio/"+capturedLMStudioModel, LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: other.URL + "/v1"})
	assertUnresolved(t, desc, err, other.URL, capturedLMStudioModel, "no loaded context length")
}

// TestResolveLocalOllamaReadsNumCtx: an Ollama model's window is its
// Modelfile's num_ctx; a model without one is unresolved, never a default and
// never the context it was trained for.
func TestResolveLocalOllamaReadsNumCtx(t *testing.T) {
	resetLocalCache(t)
	srv := ollamaServer(t, map[string][]byte{
		capturedOllamaNumCtx:   readFixture(t, ollamaNumCtxFixture),
		capturedOllamaNoNumCtx: readFixture(t, ollamaNoNumCtxFixture),
		"capped":               []byte(`{"parameters":"num_ctx                        16384\nnum_predict                    4096","capabilities":["completion"]}`),
	})
	t.Setenv(OllamaBaseURLEnv, srv.URL+"/v1")

	desc, err := ResolveLocal("opencode", "ollama/"+capturedOllamaNumCtx)
	if err != nil {
		t.Fatalf("ResolveLocal: %v", err)
	}
	want := LocalDescriptor{Endpoint: "ollama", Provider: "ollama", Model: capturedOllamaNumCtx, ContextWindow: capturedOllamaNumCtxVal, ToolCall: true}
	if desc.Reasoning == nil || !*desc.Reasoning {
		t.Errorf("reasoning = %v; the model reports the thinking capability", desc.Reasoning)
	}
	desc.Reasoning = nil
	if desc != want {
		t.Errorf("descriptor = %+v; want %+v", desc, want)
	}

	desc, err = ResolveLocal("opencode", "ollama/"+capturedOllamaNoNumCtx)
	assertUnresolved(t, desc, err, srv.URL, capturedOllamaNoNumCtx, "num_ctx", "endpoint ollama")

	desc, err = ResolveLocal("opencode", "ollama/capped")
	if err != nil {
		t.Fatalf("ResolveLocal: %v", err)
	}
	if desc.ContextWindow != 16384 || desc.MaxOutput != 4096 || desc.ToolCall || desc.Reasoning == nil || *desc.Reasoning {
		t.Errorf("descriptor = %+v; want window 16384, max output 4096 (num_predict), no tool call, reasoning false", desc)
	}

	desc, err = ResolveLocal("opencode", "ollama/not-pulled")
	assertUnresolved(t, desc, err, srv.URL, "not-pulled", "ollama pull not-pulled")
}

// TestLocalDescriptorsPerEndpoint: two endpoints serving one model id are
// discovered separately, and each keeps the window its own server reports.
func TestLocalDescriptorsPerEndpoint(t *testing.T) {
	resetLocalCache(t)
	local := lmStudioServer(t, withLoadedContext(t, 131072))
	remote := lmStudioServer(t, withLoadedContext(t, 65536))
	endpoints := []LocalEndpoint{
		{ID: "lmstudio", Provider: "lm-studio", BaseURL: local.URL + "/v1"},
		{ID: "lmstudio-remote", Provider: "lm-studio", BaseURL: remote.URL + "/v1"},
	}
	for _, tc := range []struct {
		id     string
		window int
	}{{"lmstudio", 131072}, {"lmstudio-remote", 65536}, {"lmstudio", 131072}} {
		desc, err := ResolveLocal("opencode", tc.id+"/"+capturedLMStudioModel, endpoints...)
		if err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
		if desc.Endpoint != tc.id || desc.Provider != "lm-studio" || desc.Model != capturedLMStudioModel || desc.ContextWindow != tc.window {
			t.Errorf("%s: descriptor = %+v; want endpoint %s, window %d", tc.id, desc, tc.id, tc.window)
		}
	}
	if local.hits() != 1 || remote.hits() != 1 {
		t.Errorf("hits: lmstudio %d, lmstudio-remote %d; want one each", local.hits(), remote.hits())
	}
}

// TestResolveLocalTimesOut: a server that does not answer is unresolved
// within the discovery bound.
func TestResolveLocalTimesOut(t *testing.T) {
	resetLocalCache(t)
	srv := newLocalServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	})
	start := time.Now()
	desc, err := ResolveLocal("opencode", "lmstudio/"+capturedLMStudioModel, LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: srv.URL + "/v1"})
	elapsed := time.Since(start)
	assertUnresolved(t, desc, err, srv.URL, "did not answer within 2s")
	if elapsed > LocalDiscoveryTimeout+time.Second {
		t.Errorf("discovery took %s; the bound is %s", elapsed, LocalDiscoveryTimeout)
	}
}

// TestResolveLocalRefusesAnOversizeBody: a body over 1 MiB is unresolved.
func TestResolveLocalRefusesAnOversizeBody(t *testing.T) {
	resetLocalCache(t)
	srv := newLocalServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[`))
		_, _ = w.Write(bytes.Repeat([]byte(" "), 2<<20))
		_, _ = w.Write([]byte(`]}`))
	})
	desc, err := ResolveLocal("opencode", "lmstudio/"+capturedLMStudioModel, LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: srv.URL + "/v1"})
	assertUnresolved(t, desc, err, srv.URL, "larger than 1048576 bytes")
}

// TestResolveLocalFollowsNoRedirect: a redirect is an answer, not a pointer
// to another server: the server it names is never contacted.
func TestResolveLocalFollowsNoRedirect(t *testing.T) {
	resetLocalCache(t)
	second := lmStudioServer(t, readFixture(t, lmStudioListingFixture))
	first := newLocalServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/api/v0/models", http.StatusFound)
	})
	desc, err := ResolveLocal("opencode", "lmstudio/"+capturedLMStudioModel, LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: first.URL + "/v1"})
	assertUnresolved(t, desc, err, first.URL, "HTTP 302")
	if n := second.hits(); n != 0 {
		t.Errorf("the redirect target got %d request(s); want 0", n)
	}
}

// TestResolveLocalSendsNoCredentials: no request carries a credential, even
// with provider keys in the environment, and a base URL carrying one is
// refused before any request.
func TestResolveLocalSendsNoCredentials(t *testing.T) {
	resetLocalCache(t)
	for _, name := range []string{"LMSTUDIO_API_KEY", "OPENAI_API_KEY", "OLLAMA_API_KEY"} {
		t.Setenv(name, "sentinel-key")
	}
	lm := lmStudioServer(t, readFixture(t, lmStudioListingFixture))
	ol := ollamaServer(t, map[string][]byte{capturedOllamaNumCtx: readFixture(t, ollamaNumCtxFixture)})
	endpoints := []LocalEndpoint{
		{ID: "lmstudio", Provider: "lm-studio", BaseURL: lm.URL + "/v1"},
		{ID: "ollama", Provider: "ollama", BaseURL: ol.URL + "/v1"},
	}
	if _, err := ResolveLocal("opencode", "lmstudio/"+capturedLMStudioModel, endpoints...); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLocal("opencode", "ollama/"+capturedOllamaNumCtx, endpoints...); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*localServer{lm, ol} {
		if s.hits() != 1 {
			t.Fatalf("a server got %d request(s); want 1", s.hits())
		}
		for _, r := range s.requests {
			for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
				if v := r.Header.Get(h); v != "" {
					t.Errorf("%s %s carries %s %q", r.Method, r.URL.Path, h, v)
				}
			}
		}
	}

	withUser := strings.Replace(lm.URL, "http://", "http://user:secret@", 1) + "/v1"
	desc, err := ResolveLocal("opencode", "lmstudio/other-model", LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: withUser})
	assertUnresolved(t, desc, err, lm.URL, "user name or password")
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the reason quotes the credential: %v", err)
	}
	if n := lm.hits(); n != 1 {
		t.Errorf("a base URL carrying a credential was requested: %d request(s)", n)
	}
}

// TestResolveLocalContactsOnlyTheMachineTierURL: a repository's OpenCode
// config naming another server is never read, and a declared endpoint wins
// over the environment's base URL for its id.
func TestResolveLocalContactsOnlyTheMachineTierURL(t *testing.T) {
	resetLocalCache(t)
	machine := lmStudioServer(t, readFixture(t, lmStudioListingFixture))
	repository := lmStudioServer(t, readFixture(t, lmStudioListingFixture))
	environment := lmStudioServer(t, readFixture(t, lmStudioListingFixture))

	repo := t.TempDir()
	config := `{"provider":{"lmstudio":{"options":{"baseURL":"` + repository.URL + `/v1"}}}}`
	for _, rel := range []string{"opencode.json", filepath.Join(".opencode", "opencode.json")} {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)
	t.Setenv("OPENCODE_CONFIG", filepath.Join(repo, "opencode.json"))
	t.Setenv(LMStudioBaseURLEnv, environment.URL+"/v1")

	desc, err := ResolveLocal("opencode", "lmstudio/"+capturedLMStudioModel, LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: machine.URL + "/v1"})
	if err != nil || desc.ContextWindow != capturedLoadedContext {
		t.Fatalf("ResolveLocal = %+v, %v", desc, err)
	}
	if machine.hits() != 1 || repository.hits() != 0 || environment.hits() != 0 {
		t.Errorf("hits: machine tier %d, repository %d, environment %d; want 1, 0, 0", machine.hits(), repository.hits(), environment.hits())
	}
}

// TestResolveLocalAsksOnce: a failing server is asked once per process, and
// every later call, concurrent ones included, gets the same reason.
func TestResolveLocalAsksOnce(t *testing.T) {
	resetLocalCache(t)
	srv := newLocalServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	ep := LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: srv.URL + "/v1"}
	model := "lmstudio/" + capturedLMStudioModel

	_, first := ResolveLocal("opencode", model, ep)
	desc, second := ResolveLocal("opencode", model, ep)
	assertUnresolved(t, desc, second, srv.URL, "HTTP 500", capturedLMStudioModel)
	if first == nil || first.Error() != second.Error() {
		t.Errorf("the two calls disagree: %v; %v", first, second)
	}
	if n := srv.hits(); n != 1 {
		t.Errorf("the server got %d request(s) across two calls; want exactly 1", n)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _ = ResolveLocal("opencode", "lmstudio/concurrent", ep) })
	}
	wg.Wait()
	if n := srv.hits(); n != 2 {
		t.Errorf("eight concurrent calls for one model sent %d request(s); want 1", n-1)
	}
}

// TestResolveLocalFailuresAreReasons: a refused connection, an unparseable
// body and a request that cannot be made are each unresolved with a reason,
// and none panics.
func TestResolveLocalFailuresAreReasons(t *testing.T) {
	resetLocalCache(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + listener.Addr().String()
	_ = listener.Close()
	garbage := newLocalServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	})
	garbageOllama := newLocalServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	})
	for _, tc := range []struct {
		name string
		ep   LocalEndpoint
		want string
	}{
		{"refused", LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: closed + "/v1"}, "refused the connection"},
		{"unparseable LM Studio", LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: garbage.URL + "/v1"}, "not LM Studio's model listing"},
		{"unparseable Ollama", LocalEndpoint{ID: "ollama", Provider: "ollama", BaseURL: garbageOllama.URL + "/v1"}, "not Ollama's model details"},
		{"not a URL", LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: "http://[::1"}, "not a valid URL"},
		{"not http", LocalEndpoint{ID: "lmstudio", Provider: "lm-studio", BaseURL: "ftp://localhost:1234/v1"}, "not an http or https URL"},
		{"not a local kind", LocalEndpoint{ID: "lmstudio", Provider: "openai-compatible", BaseURL: garbage.URL}, "reports no model descriptor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.ep.ID
			desc, err := ResolveLocal("opencode", key+"/"+capturedLMStudioModel, tc.ep)
			assertUnresolved(t, desc, err, closed, tc.want)
		})
	}
}

// TestResolveLocalOnlyForLocalOpenCodeModels: a model that is not on a
// server the operator runs, or has no server to ask, is unresolved without a
// request.
func TestResolveLocalOnlyForLocalOpenCodeModels(t *testing.T) {
	resetLocalCache(t)
	t.Setenv(LMStudioBaseURLEnv, "")
	t.Setenv(OllamaBaseURLEnv, "")
	for _, tc := range []struct {
		adapter, model, want string
	}{
		{"lm-studio", "lmstudio/" + capturedLMStudioModel, "opencode adapter only"},
		{"opencode", "anthropic/claude-sonnet-5", "not on a model server you run"},
		{"opencode", "lmstudio-remote/" + capturedLMStudioModel, "neither a declared endpoint"},
		{"opencode", "sonnet", "not a <provider>/<model>"},
		{"opencode", "lmstudio/" + capturedLMStudioModel, LMStudioBaseURLEnv + " is not set"},
		{"opencode", "ollama/" + capturedOllamaNumCtx, OllamaBaseURLEnv + " is not set"},
	} {
		desc, err := ResolveLocal(tc.adapter, tc.model)
		assertUnresolved(t, desc, err, "", tc.want)
	}
}
