package adapters

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// openCodeModelsCatalogGzip is the models catalog opencode 2.2.7 bundles, the
// document it reads when no file is named and nothing is cached, gzipped
// (testdata/opencode-cli/README.md says how it is captured). It is pinned, not
// fetched: every run and every probe hands OpenCode this file
// (openCodeModelsPathEnv), and the credential policy (openCodeCatalogEnv) and
// the anthropic id pin (openCodeAnthropicModels) are derived from it, so a run
// reads exactly the catalog its policy was derived from (ADR-022 § 8, § 20).
//
//go:embed opencode-models-catalog.json.gz
var openCodeModelsCatalogGzip []byte

// openCodeModelsPathEnv names the catalog file OpenCode 2.2.7 reads in place
// of its cache, its bundled snapshot and models.dev. Read from the 2.2.7
// binary: models:{file:OPENCODE_MODELS_PATH,fetch:!OPENCODE_DISABLE_MODELS_FETCH}.
// OPENCODE_DISABLE_MODELS_FETCH stays set beside it (openCodeDisableFlags).
const openCodeModelsPathEnv = "OPENCODE_MODELS_PATH"

// openCodeModelsCatalogFile is the pinned catalog's name in a run root or a
// probe directory. It matches none of openCodeCatalogCacheFiles, so the cache
// sweep never removes it.
const openCodeModelsCatalogFile = "nightgauge-models-catalog.json"

// openCodeCodeBoundEnv are the provider bindings opencode 2.2.7 makes in code
// that its catalog does not declare. Observed with each catalog variable set
// alone (TestOpenCodeCatalogEnvMatchesTheBinary): CLOUDFLARE_API_TOKEN, which
// the catalog binds to cloudflare-ai-gateway only, also loads
// cloudflare-workers-ai.
var openCodeCodeBoundEnv = map[string][]string{
	"cloudflare-workers-ai": {"CLOUDFLARE_API_TOKEN"},
}

// openCodeListedModelTypes are the model types 2.2.7 lists; it drops a
// catalog model of any other type.
var openCodeListedModelTypes = []string{"chat", "decision", "image"}

// openCodeUnlistedModelStatus is the catalog status 2.3.2 does not list: with
// ANTHROPIC_API_KEY set it lists neither claude-sonnet-4-5 nor
// claude-sonnet-4-5-20250929, the two anthropic models marked "deprecated" in
// its bundled catalog (TestOpenCodeAnthropicModelsMatchTheBinary, #3608).
const openCodeUnlistedModelStatus = "deprecated"

// openCodeModelsCatalogReader streams the pinned catalog's JSON out of the
// embedded gzip. The caller closes it.
func openCodeModelsCatalogReader() (io.ReadCloser, error) {
	zr, err := gzip.NewReader(bytes.NewReader(openCodeModelsCatalogGzip))
	if err != nil {
		return nil, fmt.Errorf("opencode models catalog: %w", err)
	}
	return zr, nil
}

// openCodeModelsCatalogJSON returns the pinned catalog's JSON. Nothing holds
// it after the call: a run only needs the file (writeOpenCodeModelsCatalog).
func openCodeModelsCatalogJSON() ([]byte, error) {
	zr, err := openCodeModelsCatalogReader()
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("opencode models catalog: %w", err)
	}
	return data, nil
}

// openCodeCatalogProvider is the part of a pinned catalog provider the
// snapshots are derived from.
type openCodeCatalogProvider struct {
	ID     string                          `json:"id"`
	Env    []string                        `json:"env"`
	Models map[string]openCodeCatalogModel `json:"models"`
}

// openCodeCatalogModel is the part of a pinned catalog model the anthropic id
// pin is derived from.
type openCodeCatalogModel struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Status       string `json:"status"`
	Experimental *struct {
		Modes map[string]json.RawMessage `json:"modes"`
	} `json:"experimental"`
}

// openCodePinnedCatalog parses the pinned catalog (openCodeParseCatalog).
func openCodePinnedCatalog() (env map[string][]string, anthropic map[string]string, err error) {
	data, err := openCodeModelsCatalogJSON()
	if err != nil {
		return nil, nil, err
	}
	return openCodeParseCatalog(data, openCodeCodeBoundEnv)
}

// openCodeParseCatalog parses a models catalog into what openCodeCatalogEnv
// and openCodeAnthropicModels hold: each provider key's variables (the
// catalog's, then codeBound's), and each anthropic model 2.2.7 lists mapped to
// the id it sends.
func openCodeParseCatalog(data []byte, codeBound map[string][]string) (env map[string][]string, anthropic map[string]string, err error) {
	var providers map[string]openCodeCatalogProvider
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, nil, fmt.Errorf("opencode models catalog: %w", err)
	}
	env = make(map[string][]string, len(providers))
	for key, p := range providers {
		if p.ID != key {
			return nil, nil, fmt.Errorf("opencode models catalog: provider %q has id %q", key, p.ID)
		}
		vars := slices.Clone(p.Env)
		for _, v := range codeBound[key] {
			if !slices.Contains(vars, v) {
				vars = append(vars, v)
			}
		}
		env[key] = vars
	}
	for key := range codeBound {
		if _, ok := providers[key]; !ok {
			return nil, nil, fmt.Errorf("opencode models catalog: a code binding names %q, which the catalog does not list", key)
		}
	}
	anthropic = map[string]string{}
	for _, m := range providers[openCodeAnthropicKey].Models {
		if !slices.Contains(openCodeListedModelTypes, m.Type) || m.Status == openCodeUnlistedModelStatus {
			continue
		}
		anthropic[m.ID] = m.ID
		if m.Experimental != nil {
			for mode := range m.Experimental.Modes {
				anthropic[m.ID+"-"+mode] = m.ID
			}
		}
	}
	return env, anthropic, nil
}

// OpenCodeModelsCatalogPath is where the pinned catalog sits in dir, a run
// root or a probe directory.
func OpenCodeModelsCatalogPath(dir string) string {
	return filepath.Join(dir, openCodeModelsCatalogFile)
}

// writeOpenCodeModelsCatalog writes the pinned catalog, mode 0600, to
// OpenCodeModelsCatalogPath(dir). It is written to a temporary name and
// renamed into place, so a link planted at the name is replaced, never
// followed, and a file a stage changed is replaced with the pinned bytes on
// every dispatch.
func writeOpenCodeModelsCatalog(dir string) error {
	zr, err := openCodeModelsCatalogReader()
	if err != nil {
		return err
	}
	defer zr.Close()
	tmp, err := os.CreateTemp(dir, ".models-catalog-*")
	if err != nil {
		return fmt.Errorf("opencode models catalog: %w", err)
	}
	_, writeErr := io.Copy(tmp, zr)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("opencode models catalog: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("opencode models catalog: %w", err)
	}
	if err := os.Rename(tmp.Name(), OpenCodeModelsCatalogPath(dir)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("opencode models catalog: %w", err)
	}
	return nil
}
