package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Local model descriptors (ADR-022 § 13, #1633).
//
// The registry holds no model of a server the operator runs. What such a
// model can hold is discovered at run time, from the server itself, and kept
// in this process only: it is never written to the registry files. A
// descriptor is keyed by the endpoint it was discovered on and the model id
// on it, so two endpoints serving the same model each keep their own.
//
// LM Studio is read from GET /api/v1/models when the server resolves the
// model there (#1761): the entry's key, capabilities.trained_for_tool_use,
// capabilities.reasoning and its loaded instance's config.context_length.
// Otherwise it is read from the older GET /api/v0/models: the model's state,
// loaded_context_length and capabilities. Either way the window is the one a
// session has, never max_context_length, which is only what the model could
// be loaded with. Ollama is read from POST /api/show: the num_ctx parameter of
// the model's Modelfile. Observed on Ollama 0.32.11, a model whose Modelfile
// sets no num_ctx is loaded with a default context that /api/show does not
// report (GET /api/ps reports it only while the model is loaded), so its
// descriptor is unresolved.
//
// Discovery asks once per endpoint and model per process: one request for
// Ollama, and for LM Studio a v1 request, then a v0 request only when v1 did
// not resolve the model, both within one LocalDiscoveryTimeout. It asks the
// base URL of an endpoint the machine-tier `opencode:` block declares or, for
// the default endpoints, the one NIGHTGAUGE_LM_STUDIO_BASE_URL or
// NIGHTGAUGE_OLLAMA_BASE_URL names. Nothing a repository holds is read for
// it. A request carries no credential, uses no proxy, follows no redirect,
// and its body is bounded by localMaxBody. An endpoint's
// address never appears in a descriptor or a reason: they name the endpoint
// by its id.

// LocalDiscoveryTimeout bounds one discovery on a server: Ollama's one
// request, or LM Studio's v1 request (capped at half of it) and its v0
// fallback together.
const LocalDiscoveryTimeout = 2 * time.Second

// localMaxBody bounds a server's response. A model listing is a few
// kilobytes, and Ollama's /api/show without verbose is tens of kilobytes.
const localMaxBody = 1 << 20

// The variables naming a default endpoint's base URL when the machine-tier
// config declares no endpoint with its id.
const (
	LMStudioBaseURLEnv = "NIGHTGAUGE_LM_STUDIO_BASE_URL"
	OllamaBaseURLEnv   = "NIGHTGAUGE_OLLAMA_BASE_URL"
)

var localBaseURLEnv = map[string]string{
	"lm-studio": LMStudioBaseURLEnv,
	"ollama":    OllamaBaseURLEnv,
}

// LocalEndpoint is a model server the machine-tier `opencode:` block
// declares.
type LocalEndpoint struct {
	// ID is the endpoint id, the provider key a stage names on -m.
	ID string
	// Provider is the endpoint's kind: lm-studio or ollama.
	Provider string
	// BaseURL is the server's OpenAI-compatible API root. It is requested and
	// never reported.
	BaseURL string
	// SelfHosted is the entry's self_hosted declaration (#1679): nil when the
	// entry does not set it, so locality follows Provider and BaseURL; false
	// marks an endpoint that forwards to a hosted service (a LiteLLM proxy on
	// loopback, say), which is never local whatever its address; true is the
	// operator's statement that the model runs on that server.
	SelfHosted *bool
}

// LocalDescriptor is what a model on a local server can hold, as the server
// reports it.
type LocalDescriptor struct {
	// Endpoint is the id of the endpoint the descriptor was discovered on.
	Endpoint string `json:"endpoint"`
	// Provider is the normalized provider: lm-studio or ollama.
	Provider string `json:"provider"`
	// Model is the model id on the endpoint, without its provider key.
	Model string `json:"model"`
	// ContextWindow is the context the server loads the model with: LM
	// Studio's loaded instance config.context_length (v1) or
	// loaded_context_length (v0), Ollama's num_ctx. It is never 0 in a
	// resolved descriptor.
	ContextWindow int `json:"context_window"`
	// MaxOutput is the most tokens the server lets one reply use: Ollama's
	// num_predict when the Modelfile sets one. 0 means the server reports no
	// cap below the window, which LM Studio never reports.
	MaxOutput int `json:"max_output"`
	// ToolCall is whether the server says the model calls tools: LM Studio's
	// capabilities.trained_for_tool_use (v1) or tool_use capability (v0),
	// Ollama's tools capability.
	ToolCall bool `json:"tool_call"`
	// Reasoning is whether the server says the model reasons: LM Studio's
	// v1 capabilities.reasoning, Ollama's thinking capability. nil means the
	// server does not say: an LM Studio model read from /api/v0/models, which
	// reports no such capability, or a v1 entry with no capabilities.
	Reasoning *bool `json:"reasoning,omitempty"`
}

// ResolveLocal returns the descriptor of model, a "<provider>/<model>" the
// opencode adapter dispatches, on the endpoint whose id is its provider key.
// That is the endpoint in endpoints with that id, which the machine-tier
// `opencode:` block declares, or, when none has it, the default endpoint of
// the key's normalized provider (ProviderFor: lmstudio is lm-studio, ollama is
// ollama) at the base URL NIGHTGAUGE_LM_STUDIO_BASE_URL or
// NIGHTGAUGE_OLLAMA_BASE_URL names.
//
// The first call for an endpoint and model discovers the descriptor
// (DiscoverLocal); every later call in the process returns that result,
// resolved or not, so a server that fails is asked once and never retried.
// An error is the reason the descriptor is unresolved, naming the model and
// the endpoint by id; a caller fails closed on it. The descriptor is never a
// default and its context window never 0.
func ResolveLocal(adapter, model string, endpoints ...LocalEndpoint) (LocalDescriptor, error) {
	return ResolveLocalContext(context.Background(), adapter, model, endpoints...)
}

// ResolveLocalContext is ResolveLocal that stops waiting when ctx is done.
// The discovery itself still runs to its own LocalDiscoveryTimeout and is
// cached as ResolveLocal's is: a caller that gave up does not leave a failed
// result behind for the next one.
func ResolveLocalContext(ctx context.Context, adapter, model string, endpoints ...LocalEndpoint) (LocalDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return LocalDescriptor{}, fmt.Errorf("the descriptor of model %s was not resolved: %w", model, err)
	}
	if adapter != openCodeAdapter {
		return LocalDescriptor{}, fmt.Errorf("local model descriptors are discovered for the %s adapter only, not %q", openCodeAdapter, adapter)
	}
	key, id, ok := splitOpenCodeModel(model)
	if !ok {
		return LocalDescriptor{}, fmt.Errorf("model %q is not a <provider>/<model>, so no endpoint serves it", model)
	}
	ep, err := localEndpointFor(adapter, model, key, endpoints)
	if err != nil {
		return LocalDescriptor{}, err
	}
	type resolved struct {
		desc LocalDescriptor
		err  error
	}
	done := make(chan resolved, 1)
	go func() {
		desc, err := discoverLocalOnce(ep, id)
		done <- resolved{desc, err}
	}()
	select {
	case r := <-done:
		return r.desc, r.err
	case <-ctx.Done():
		return LocalDescriptor{}, fmt.Errorf("the descriptor of model %s on endpoint %s was not resolved: %w", id, ep.ID, ctx.Err())
	}
}

// localEndpointFor is the endpoint a model with provider key key runs on.
func localEndpointFor(adapter, model, key string, endpoints []LocalEndpoint) (LocalEndpoint, error) {
	for _, ep := range endpoints {
		if ep.ID != key {
			continue
		}
		if !IsLocalProvider(ep.Provider) {
			return LocalEndpoint{}, fmt.Errorf("endpoint %s is of kind %q, which reports no model descriptor: only lm-studio and ollama do", ep.ID, ep.Provider)
		}
		return ep, nil
	}
	provider := ProviderFor(adapter, model)
	if !IsLocalProvider(provider) {
		return LocalEndpoint{}, fmt.Errorf("model %q is not on a model server you run: its provider key %q is neither a declared endpoint nor lmstudio or ollama", model, key)
	}
	env := localBaseURLEnv[provider]
	baseURL := os.Getenv(env)
	if baseURL == "" {
		return LocalEndpoint{}, fmt.Errorf("model %q has no server to discover it from: the machine-tier opencode: block declares no endpoint %s, and %s is not set", model, key, env)
	}
	return LocalEndpoint{ID: key, Provider: provider, BaseURL: baseURL}, nil
}

// localCacheKey identifies one discovery. The base URL is part of it so an
// endpoint re-pointed at another server is discovered again; it is never
// reported.
type localCacheKey struct {
	endpoint, provider, baseURL, model string
}

// localCacheEntry is one discovery's result. err starts non-nil, so an entry
// whose discovery never completed can never read as resolved.
type localCacheEntry struct {
	once sync.Once
	desc LocalDescriptor
	err  error
}

// localCache holds every discovery this process has made.
var localCache sync.Map // localCacheKey -> *localCacheEntry

func discoverLocalOnce(ep LocalEndpoint, model string) (LocalDescriptor, error) {
	fresh := &localCacheEntry{err: fmt.Errorf("the descriptor of model %s on endpoint %s was never discovered", model, ep.ID)}
	v, _ := localCache.LoadOrStore(localCacheKey{ep.ID, ep.Provider, ep.BaseURL, model}, fresh)
	e := v.(*localCacheEntry)
	e.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), LocalDiscoveryTimeout)
		defer cancel()
		desc, err := DiscoverLocal(ctx, ep.Provider, ep.BaseURL, model)
		if err != nil {
			e.err = fmt.Errorf("the descriptor of model %s on endpoint %s is unresolved: %w", model, ep.ID, err)
			return
		}
		desc.Endpoint = ep.ID
		e.desc, e.err = desc, nil
	})
	return e.desc, e.err
}

// DiscoverLocal asks the provider server at baseURL, once and uncached, for
// the descriptor of model, the model id on the server. The returned
// descriptor has no Endpoint; ResolveLocal sets it. An error is the reason the
// descriptor is unresolved: the server refused, did not answer within the
// bound, answered with something else than a success, a body larger than
// localMaxBody or one it cannot parse, or does not have the model with a
// known context. It never names baseURL.
func DiscoverLocal(ctx context.Context, provider, baseURL, model string) (LocalDescriptor, error) {
	root, err := localServerRoot(baseURL)
	if err != nil {
		return LocalDescriptor{}, err
	}
	switch provider {
	case "lm-studio":
		return discoverLMStudio(ctx, root, model)
	case "ollama":
		return discoverOllama(ctx, root, model)
	default:
		return LocalDescriptor{}, fmt.Errorf("provider %q has no descriptor to discover: only lm-studio and ollama servers report one", provider)
	}
}

// localServerRoot is the scheme and host of an OpenAI-compatible base URL,
// where LM Studio's and Ollama's own APIs are. A URL carrying a user name or
// password is refused: the client would send it as a credential.
func localServerRoot(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	switch {
	case err != nil:
		return "", errors.New("the base URL is not a valid URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return "", errors.New("the base URL is not an http or https URL")
	case u.User != nil:
		return "", errors.New("the base URL carries a user name or password, which discovery never sends")
	case u.Host == "":
		return "", errors.New("the base URL names no host")
	}
	return u.Scheme + "://" + u.Host, nil
}

// localClient requests only the URL it is given: no proxy from the
// environment, no redirect followed, no cookie jar, one connection per
// request.
var localClient = &http.Client{
	Timeout: LocalDiscoveryTimeout,
	Transport: &http.Transport{
		Proxy:             nil,
		DialContext:       (&net.Dialer{Timeout: LocalDiscoveryTimeout}).DialContext,
		DisableKeepAlives: true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// localRequest sends one request and returns the status and the body of a
// 200 response. A transport failure is described by its kind alone, because
// Go's error text quotes the URL.
func localRequest(ctx context.Context, method, target string, payload []byte) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, nil, errors.New("the request could not be built")
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := localClient.Do(req)
	if err != nil {
		return 0, nil, errors.New(localTransportFailure(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, fmt.Errorf("the server answered HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, localMaxBody+1))
	if err != nil {
		return resp.StatusCode, nil, errors.New("the response could not be read within the bound")
	}
	if len(data) > localMaxBody {
		return resp.StatusCode, nil, fmt.Errorf("the response is larger than %d bytes", localMaxBody)
	}
	return resp.StatusCode, data, nil
}

// localTransportFailure names the kind of a transport error without its text.
func localTransportFailure(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "the server refused the connection"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("the server did not answer within %s", LocalDiscoveryTimeout)
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "there is no route to the server"
	default:
		return "the request to the server failed"
	}
}

// lmStudioListing is LM Studio's GET /api/v0/models. max_context_length is
// deliberately not read: it is what the model could be loaded with, not the
// window a session has.
type lmStudioListing struct {
	Data []lmStudioModel `json:"data"`
}

// lmStudioModel is one entry of the listing.
type lmStudioModel struct {
	ID                  string   `json:"id"`
	State               string   `json:"state"`
	LoadedContextLength int      `json:"loaded_context_length"`
	Capabilities        []string `json:"capabilities"`
}

// lmStudioV1Listing is LM Studio's newer GET /api/v1/models (#1761), in the
// shape LM Studio's REST API documentation gives
// (https://lmstudio.ai/docs/developer/rest/list): a models[] array whose
// entries are keyed by key, with capabilities.trained_for_tool_use,
// capabilities.reasoning (present only when the model exposes a reasoning
// configuration) and loaded_instances[].config.context_length, the context
// each loaded instance runs with. max_context_length is deliberately not
// read, for the same reason as v0's. No LM Studio server was reachable to
// capture a live v1 response when this was written: the fixture
// testdata/local-discovery/lmstudio-api-v1-models.json is transcribed from
// that documentation and must be re-captured (README.md there).
type lmStudioV1Listing struct {
	Models []lmStudioV1Model `json:"models"`
}

// lmStudioV1Model is one entry of the v1 listing. Capabilities is absent for
// an embedding model.
type lmStudioV1Model struct {
	Key          string `json:"key"`
	Capabilities *struct {
		TrainedForToolUse bool `json:"trained_for_tool_use"`
		Reasoning         *struct {
			AllowedOptions []string `json:"allowed_options"`
			Default        string   `json:"default"`
		} `json:"reasoning"`
	} `json:"capabilities"`
	LoadedInstances []struct {
		ID     string `json:"id"`
		Config struct {
			ContextLength int `json:"context_length"`
		} `json:"config"`
	} `json:"loaded_instances"`
}

// lmStudioV0Unanswered is a v0 failure in which the server gave no model
// listing at all (a transport failure, a non-200 status, or a body that is
// not the listing), as opposed to a listing that does not resolve the model.
type lmStudioV0Unanswered struct{ err error }

func (e lmStudioV0Unanswered) Error() string { return e.err.Error() }
func (e lmStudioV0Unanswered) Unwrap() error { return e.err }

// discoverLMStudio prefers the newer GET /api/v1/models, the only source of
// Reasoning, and falls back to GET /api/v0/models whenever v1 does not
// resolve the model: a transport failure, a non-200 status, a body that does
// not decode as lmStudioV1Listing, or a listing that does not list the model,
// has not loaded it or reports no context length for it. A v1 answer never
// hides a v0 answer that resolves. Both requests share the one
// LocalDiscoveryTimeout budget ctx carries; the v1 request is capped at half
// of it, so a server that accepts the v1 connection but never answers cannot
// starve the v0 fallback. When neither resolves, the reason is v0's, unless
// v0 gave no listing at all and v1 did, in which case it is v1's.
func discoverLMStudio(ctx context.Context, root, model string) (LocalDescriptor, error) {
	v1Ctx, cancel := context.WithTimeout(ctx, LocalDiscoveryTimeout/2)
	listing, ok := lmStudioV1(v1Ctx, root)
	cancel()
	var v1Err error
	if ok {
		desc, err := lmStudioFromV1(listing, model)
		if err == nil {
			return desc, nil
		}
		v1Err = err
	}
	desc, err := discoverLMStudioV0(ctx, root, model)
	if err != nil && v1Err != nil && errors.As(err, new(lmStudioV0Unanswered)) {
		return LocalDescriptor{}, v1Err
	}
	return desc, err
}

// lmStudioV1 asks root's GET /api/v1/models and reports ok=true only when the
// server answered with a body that decodes as lmStudioV1Listing.
func lmStudioV1(ctx context.Context, root string) (lmStudioV1Listing, bool) {
	_, body, err := localRequest(ctx, http.MethodGet, root+"/api/v1/models", nil)
	if err != nil {
		return lmStudioV1Listing{}, false
	}
	var listing lmStudioV1Listing
	if jsonErr := json.Unmarshal(body, &listing); jsonErr != nil || listing.Models == nil {
		return lmStudioV1Listing{}, false
	}
	return listing, true
}

// lmStudioFromV1 is discoverLMStudio's v1 half. The model is the entry whose
// key is model; it is loaded when it has a loaded instance, and its window is
// the first loaded instance's config.context_length. ToolCall is
// capabilities.trained_for_tool_use. Reasoning is nil when the entry has no
// capabilities (an embedding model), false when capabilities carries no
// reasoning object, and otherwise whether any allowed reasoning option is
// other than "off".
func lmStudioFromV1(listing lmStudioV1Listing, model string) (LocalDescriptor, error) {
	i := slices.IndexFunc(listing.Models, func(m lmStudioV1Model) bool { return m.Key == model })
	if i < 0 {
		return LocalDescriptor{}, fmt.Errorf("the LM Studio server does not list model %s: download it with `lms get %s`", model, model)
	}
	m := listing.Models[i]
	if len(m.LoadedInstances) == 0 {
		return LocalDescriptor{}, fmt.Errorf("the LM Studio server lists model %s but has not loaded it, and a model it loads on demand gets its default context: load it with `lms load %s`", model, model)
	}
	contextWindow := m.LoadedInstances[0].Config.ContextLength
	if contextWindow <= 0 {
		return LocalDescriptor{}, fmt.Errorf("the LM Studio server reports no loaded context length for model %s", model)
	}
	desc := LocalDescriptor{
		Provider:      "lm-studio",
		Model:         model,
		ContextWindow: contextWindow,
	}
	if m.Capabilities != nil {
		desc.ToolCall = m.Capabilities.TrainedForToolUse
		reasoning := false
		if r := m.Capabilities.Reasoning; r != nil {
			reasoning = slices.ContainsFunc(r.AllowedOptions, func(o string) bool { return o != "off" })
		}
		desc.Reasoning = &reasoning
	}
	return desc, nil
}

// discoverLMStudioV0 is discoverLMStudio's fallback: the original
// /api/v0/models path, which reports no Reasoning. A failure in which the
// server gave no listing is a lmStudioV0Unanswered.
func discoverLMStudioV0(ctx context.Context, root, model string) (LocalDescriptor, error) {
	_, body, err := localRequest(ctx, http.MethodGet, root+"/api/v0/models", nil)
	if err != nil {
		return LocalDescriptor{}, lmStudioV0Unanswered{fmt.Errorf("the LM Studio model listing: %w", err)}
	}
	var listing lmStudioListing
	if err := json.Unmarshal(body, &listing); err != nil || listing.Data == nil {
		return LocalDescriptor{}, lmStudioV0Unanswered{errors.New("the server's /api/v0/models is not LM Studio's model listing")}
	}
	i := slices.IndexFunc(listing.Data, func(m lmStudioModel) bool { return m.ID == model })
	if i < 0 {
		return LocalDescriptor{}, fmt.Errorf("the LM Studio server does not list model %s: download it with `lms get %s`", model, model)
	}
	m := listing.Data[i]
	if m.State != "loaded" {
		return LocalDescriptor{}, fmt.Errorf("the LM Studio server lists model %s but has not loaded it (state %q), and a model it loads on demand gets its default context: load it with `lms load %s`", model, m.State, model)
	}
	if m.LoadedContextLength <= 0 {
		return LocalDescriptor{}, fmt.Errorf("the LM Studio server reports no loaded context length for model %s", model)
	}
	return LocalDescriptor{
		Provider:      "lm-studio",
		Model:         model,
		ContextWindow: m.LoadedContextLength,
		ToolCall:      slices.Contains(m.Capabilities, "tool_use"),
	}, nil
}

// ollamaShow is the part of Ollama's POST /api/show discovery reads.
// model_info's <arch>.context_length is deliberately not read: it is the
// context the model was trained for, not the one Ollama loads it with.
type ollamaShow struct {
	// Parameters is the Modelfile's parameters, one "name value" per line.
	Parameters   string   `json:"parameters"`
	Capabilities []string `json:"capabilities"`
}

func discoverOllama(ctx context.Context, root, model string) (LocalDescriptor, error) {
	payload, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return LocalDescriptor{}, errors.New("the request could not be built")
	}
	status, body, err := localRequest(ctx, http.MethodPost, root+"/api/show", payload)
	switch {
	case status == http.StatusNotFound:
		return LocalDescriptor{}, fmt.Errorf("the Ollama server does not have model %s: pull it with `ollama pull %s`", model, model)
	case err != nil:
		return LocalDescriptor{}, fmt.Errorf("the Ollama model details: %w", err)
	}
	var show ollamaShow
	if err := json.Unmarshal(body, &show); err != nil {
		return LocalDescriptor{}, errors.New("the server's /api/show is not Ollama's model details")
	}
	numCtx := ollamaParameter(show.Parameters, "num_ctx")
	if numCtx <= 0 {
		return LocalDescriptor{}, fmt.Errorf("the Ollama server reports no num_ctx for model %s: its Modelfile sets none, so the server loads it with a default context /api/show does not report; set PARAMETER num_ctx in its Modelfile, or opencode.limit.context", model)
	}
	desc := LocalDescriptor{
		Provider:      "ollama",
		Model:         model,
		ContextWindow: numCtx,
		MaxOutput:     max(0, ollamaParameter(show.Parameters, "num_predict")),
		ToolCall:      slices.Contains(show.Capabilities, "tools"),
	}
	if show.Capabilities != nil {
		reasoning := slices.Contains(show.Capabilities, "thinking")
		desc.Reasoning = &reasoning
	}
	return desc, nil
}

// ollamaParameter is the integer value of parameter name in an /api/show
// parameters block, or 0 when it is absent or not an integer.
func ollamaParameter(parameters, name string) int {
	for line := range strings.SplitSeq(parameters, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == name {
			n, err := strconv.Atoi(fields[1])
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// IsLocalBaseURL reports whether baseURL is a server the operator runs: an
// http or https URL whose host is localhost, a loopback address or a
// private-network address (RFC 1918, IPv6 unique-local). A host name other
// than localhost is never resolved, so it is not local. A URL carrying a user
// name or password is not local: it is refused as an endpoint anyway (#2128).
// Mirrors isLocalBaseUrl in the SDK modelRegistry.ts.
func IsLocalBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// IsLocalEndpoint reports whether a declared endpoint (#1678) is a model
// server the operator runs. An explicit self_hosted declaration decides
// (#1679): false is an endpoint that forwards to a hosted service, such as a
// loopback proxy to a hosted API, and it is never local. Otherwise its kind
// is lm-studio or ollama, or its base URL is local (IsLocalBaseURL), whatever
// brand its id carries.
func IsLocalEndpoint(ep LocalEndpoint) bool {
	if ep.SelfHosted != nil {
		return *ep.SelfHosted
	}
	return IsLocalProvider(ep.Provider) || IsLocalBaseURL(ep.BaseURL)
}

// IsOllamaCloudModel reports whether id, a model id on an Ollama server, is
// one Ollama serves from its hosted service through the same local API: its
// tag is "cloud" or ends in "-cloud" (ADR-022 § 3, #1679), as in
// "gpt-oss:120b-cloud" or "qwen3-coder:cloud". Such a model does not run on
// the endpoint, so it is never local.
func IsOllamaCloudModel(id string) bool {
	_, tag, tagged := strings.Cut(id, ":")
	if !tagged {
		return strings.HasSuffix(id, "-cloud")
	}
	return tag == "cloud" || strings.HasSuffix(tag, "-cloud")
}

// IsLocalModel reports whether model, dispatched on adapter, runs on a server
// the operator runs (#2128). Locality follows the declared endpoint, not the
// provider-key brand: an opencode model whose provider key is the id of a
// declared endpoint is local exactly when that endpoint is
// (IsLocalEndpoint), so omlx/... on a declared loopback endpoint is local.
// An undeclared key is local only when ProviderFor normalizes it to a local
// provider; "other" stays non-local. An Ollama cloud model
// (IsOllamaCloudModel) on an ollama endpoint or key is never local (#1679).
// Mirrors isLocalModel in the SDK modelRegistry.ts.
func IsLocalModel(adapter, model string, endpoints []LocalEndpoint) bool {
	if adapter == openCodeAdapter {
		key, id, ok := splitOpenCodeModel(model)
		if !ok {
			return false
		}
		for _, ep := range endpoints {
			if ep.ID == key {
				if ep.Provider == "ollama" && IsOllamaCloudModel(id) {
					return false
				}
				return IsLocalEndpoint(ep)
			}
		}
		if ProviderFor(adapter, model) == "ollama" && IsOllamaCloudModel(id) {
			return false
		}
	}
	return IsLocalProvider(ProviderFor(adapter, model))
}
