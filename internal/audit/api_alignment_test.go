package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// --- Normalizer tests ---

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"/api/teams/:id", "/api/teams/{id}"},
		{"/api/v1/auth/verify", "/api/v1/auth/verify"},
		{"/api/v1/pipeline/run", "/api/v1/pipeline/run"},
		{"/api/v1/health", "/api/v1/health"},
		{"/api/projects/{id}", "/api/projects/{id}"},
		{"/api/billing/current", "/api/billing/current"},
		{"/api/teams/", "/api/teams"},
		{"api/test", "/api/test"},
	}
	for _, tc := range cases {
		got := NormalizePath(tc.input)
		if got != tc.expected {
			t.Errorf("NormalizePath(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestPathsMatch(t *testing.T) {
	if !PathsMatch("/api/teams/:id", "/api/teams/{id}") {
		t.Error("expected :id and {id} param styles to match")
	}
	if PathsMatch("/api/teams", "/api/team") {
		t.Error("singular/plural should not match")
	}
}

func TestPathSimilarity(t *testing.T) {
	similar, detail := PathSimilarity("/api/v1/auth/verify", "/api/auth/verify")
	if !similar {
		t.Error("expected version-prefixed path to be similar to canonical")
	}
	if detail == "" {
		t.Error("expected non-empty detail for version prefix similarity")
	}
}

// --- Angular extractor tests ---

func TestAngularExtractor(t *testing.T) {
	dir := t.TempDir()
	apiDir := filepath.Join(dir, "src", "app", "core", "api")
	if err := os.MkdirAll(apiDir, 0755); err != nil {
		t.Fatal(err)
	}

	svcContent := `
import { HttpClient } from '@angular/common/http';
@Injectable()
export class AuthService {
  constructor(private http: HttpClient) {}

  verify() {
    return this.http.get<any>('/api/auth/verify', { headers: this.getAuthHeaders() });
  }

  login(body: any) {
    return this.http.post<any>('/api/auth/login', body);
  }

  updateProject(id: string, body: any) {
    return this.http.put<any>('/api/projects/' + id, body, { headers: this.getAuthHeaders() });
  }
}
`
	if err := os.WriteFile(filepath.Join(apiDir, "auth.service.ts"), []byte(svcContent), 0644); err != nil {
		t.Fatal(err)
	}

	endpoints, ok := ExtractAngularEndpoints(dir)
	if !ok {
		t.Fatal("expected ok=true for accessible angular repo")
	}
	if len(endpoints) < 2 {
		t.Errorf("expected at least 2 endpoints, got %d", len(endpoints))
	}

	// Verify auth detection
	for _, ep := range endpoints {
		if ep.Path == "/api/auth/verify" && ep.AuthType != "bearer" {
			t.Errorf("expected bearer auth for verify endpoint, got %s", ep.AuthType)
		}
	}
}

func TestAngularExtractorMissingRepo(t *testing.T) {
	_, ok := ExtractAngularEndpoints("/nonexistent/path")
	if ok {
		t.Error("expected ok=false for missing repo")
	}
}

// --- Flutter extractor tests ---

func TestFlutterExtractor(t *testing.T) {
	dir := t.TempDir()
	apiDir := filepath.Join(dir, "lib", "core", "network", "api")
	if err := os.MkdirAll(apiDir, 0755); err != nil {
		t.Fatal(err)
	}

	dartContent := `
class AuthApi {
  final Dio _dio;
  AuthApi(this._dio);

  Future<Response> verifyToken() async {
    final headers = await getAuthHeaders();
    return await _dio.get('/api/v1/auth/verify', options: Options(headers: headers));
  }

  Future<Response> getBillingCurrent() async {
    return await _dio.get('/api/billing/current');
  }

  Future<Response> updateProject(String id) async {
    return await _dio.put('/api/projects/$id');
  }
}
`
	if err := os.WriteFile(filepath.Join(apiDir, "auth_api.dart"), []byte(dartContent), 0644); err != nil {
		t.Fatal(err)
	}

	endpoints, ok := ExtractFlutterEndpoints(dir)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(endpoints) < 3 {
		t.Errorf("expected at least 3 endpoints, got %d", len(endpoints))
	}

	// Check that /api/v1/auth/verify is captured
	found := false
	for _, ep := range endpoints {
		if ep.Path == "/api/v1/auth/verify" {
			found = true
			if ep.Method != "GET" {
				t.Errorf("expected GET method, got %s", ep.Method)
			}
			break
		}
	}
	if !found {
		t.Error("expected to find /api/v1/auth/verify endpoint")
	}
}

func TestFlutterDirectGitHubDetection(t *testing.T) {
	dir := t.TempDir()
	networkDir := filepath.Join(dir, "lib", "core", "network")
	if err := os.MkdirAll(networkDir, 0755); err != nil {
		t.Fatal(err)
	}

	dartContent := `
class IssueRemoteDataSource {
  Future<List<Issue>> fetchIssues(String owner, String repo) async {
    final response = await http.get(
      Uri.parse('https://api.github.com/repos/$owner/$repo/issues'),
      headers: {'Authorization': 'token $token'},
    );
    return parseIssues(response.body);
  }
}
`
	if err := os.WriteFile(filepath.Join(networkDir, "issue_remote_data_source.dart"), []byte(dartContent), 0644); err != nil {
		t.Fatal(err)
	}

	findings, ok := ExtractFlutterDirectGitHubCalls(dir)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(findings) == 0 {
		t.Error("expected at least one DIRECT_GITHUB_CALL finding")
	}
	if findings[0].Category != "DIRECT_GITHUB_CALL" {
		t.Errorf("expected DIRECT_GITHUB_CALL category, got %s", findings[0].Category)
	}
	if findings[0].Severity != "info" {
		t.Errorf("expected info severity, got %s", findings[0].Severity)
	}
}

// --- Platform extractor tests ---

func TestPlatformExtractor(t *testing.T) {
	dir := t.TempDir()
	routesDir := filepath.Join(dir, "packages", "api", "src", "routes")
	if err := os.MkdirAll(routesDir, 0755); err != nil {
		t.Fatal(err)
	}

	routesContent := `
import { authRequired } from '../middleware/auth';

app.get('/api/auth/verify', authRequired, verifyHandler);
app.post('/api/auth/login', loginHandler);
app.get('/api/health', healthHandler);
app.get('/api/billing/current', authRequired, billingHandler);
app.patch('/api/projects/:id', authRequired, updateProjectHandler);
app.get('/api/analytics', authRequired, analyticsHandler);
`
	if err := os.WriteFile(filepath.Join(routesDir, "routes.ts"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	canonical, ok := ExtractPlatformRoutes(dir)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(canonical) < 5 {
		t.Errorf("expected at least 5 canonical routes, got %d", len(canonical))
	}

	// Check /api/auth/verify exists
	key := "GET:/api/auth/verify"
	if _, exists := canonical[key]; !exists {
		t.Errorf("expected canonical route %s, keys: %v", key, mapKeys(canonical))
	}

	// Check auth detection
	billingKey := "GET:/api/billing/current"
	if ep, exists := canonical[billingKey]; exists {
		if ep.AuthType != "bearer" {
			t.Errorf("expected bearer auth for billing, got %s", ep.AuthType)
		}
	}
}

func mapKeys(m map[string]CanonicalEndpoint) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// --- Alignment classification tests ---

func TestAlignmentClassification_PathMismatch(t *testing.T) {
	canonical := map[string]CanonicalEndpoint{
		"GET:/api/auth/verify": {
			Path: "/api/auth/verify", Method: "GET", AuthType: "bearer",
		},
	}

	ep := ClientEndpoint{
		Client: "flutter", Path: "/api/v1/auth/verify", Method: "GET", AuthType: "bearer",
		SourceFile: "test.dart", LineNumber: 1,
	}

	findings := classifyEndpoint(ep, canonical)
	if len(findings) == 0 {
		t.Fatal("expected at least one finding for version mismatch")
	}
	if findings[0].Category != "PATH_MISMATCH" {
		t.Errorf("expected PATH_MISMATCH, got %s", findings[0].Category)
	}
}

func TestAlignmentClassification_MethodMismatch(t *testing.T) {
	canonical := map[string]CanonicalEndpoint{
		"PATCH:/api/projects/{id}": {
			Path: "/api/projects/:id", Method: "PATCH", AuthType: "bearer",
		},
	}

	ep := ClientEndpoint{
		Client: "flutter", Path: "/api/projects/{id}", Method: "PUT", AuthType: "bearer",
		SourceFile: "test.dart", LineNumber: 1,
	}

	findings := classifyEndpoint(ep, canonical)
	if len(findings) == 0 {
		t.Fatal("expected at least one finding for method mismatch")
	}
	if findings[0].Category != "METHOD_MISMATCH" {
		t.Errorf("expected METHOD_MISMATCH, got %s", findings[0].Category)
	}
}

func TestAlignmentClassification_AuthMismatch(t *testing.T) {
	canonical := map[string]CanonicalEndpoint{
		"GET:/api/billing/current": {
			Path: "/api/billing/current", Method: "GET", AuthType: "bearer",
		},
	}

	ep := ClientEndpoint{
		Client: "flutter", Path: "/api/billing/current", Method: "GET", AuthType: "none",
		SourceFile: "test.dart", LineNumber: 1,
	}

	findings := classifyEndpoint(ep, canonical)
	if len(findings) == 0 {
		t.Fatal("expected at least one finding for auth mismatch")
	}
	if findings[0].Category != "AUTH_MISMATCH" {
		t.Errorf("expected AUTH_MISMATCH, got %s", findings[0].Category)
	}
}

func TestAlignmentClassification_NotFound(t *testing.T) {
	canonical := map[string]CanonicalEndpoint{
		"GET:/api/health": {Path: "/api/health", Method: "GET", AuthType: "none"},
	}

	ep := ClientEndpoint{
		Client: "flutter", Path: "/api/unused", Method: "GET", AuthType: "none",
		SourceFile: "test.dart", LineNumber: 1,
	}

	findings := classifyEndpoint(ep, canonical)
	if len(findings) == 0 {
		t.Fatal("expected at least one NOT_FOUND finding")
	}
	if findings[0].Category != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %s", findings[0].Category)
	}
}

func TestAlignmentClassification_PerfectMatch(t *testing.T) {
	canonical := map[string]CanonicalEndpoint{
		"GET:/api/health": {Path: "/api/health", Method: "GET", AuthType: "none"},
	}

	ep := ClientEndpoint{
		Client: "flutter", Path: "/api/health", Method: "GET", AuthType: "none",
		SourceFile: "test.dart", LineNumber: 1,
	}

	findings := classifyEndpoint(ep, canonical)
	if len(findings) != 0 {
		t.Errorf("expected no findings for perfect match, got %d: %+v", len(findings), findings)
	}
}

// --- Full 8-mismatch regression simulation ---

func TestRegression8FlutterMismatches(t *testing.T) {
	// Build a minimal in-memory test using temp repos to simulate the 8 known mismatches.
	dir := t.TempDir()

	// Platform routes
	routesDir := filepath.Join(dir, "platform", "packages", "api", "src", "routes")
	if err := os.MkdirAll(routesDir, 0755); err != nil {
		t.Fatal(err)
	}
	platformRoutes := `
app.get('/api/auth/verify', authRequired, handler);
app.post('/api/pipeline/run', authRequired, handler);
app.get('/api/health', handler);
app.get('/api/billing/current', authRequired, handler);
app.patch('/api/projects/:id', authRequired, handler);
app.get('/api/analytics', authRequired, handler);
`
	if err := os.WriteFile(filepath.Join(routesDir, "routes.ts"), []byte(platformRoutes), 0644); err != nil {
		t.Fatal(err)
	}

	// Flutter Dio API files
	flutterAPIDir := filepath.Join(dir, "flutter", "lib", "core", "network", "api")
	if err := os.MkdirAll(flutterAPIDir, 0755); err != nil {
		t.Fatal(err)
	}
	flutterDio := `
class AppApi {
  final Dio _dio;

  // PATH_MISMATCH: /v1/auth/verify vs /auth/verify
  Future<Response> verify() => _dio.get('/api/v1/auth/verify');
  // PATH_MISMATCH: /v1/pipeline/run
  Future<Response> runPipeline() => _dio.post('/api/v1/pipeline/run');
  // PATH_MISMATCH: /v1/health
  Future<Response> health() => _dio.get('/api/v1/health');
  // AUTH_MISMATCH: no auth header
  Future<Response> getBilling() => _dio.get('/api/billing/current');
  // METHOD_MISMATCH: PUT vs PATCH
  Future<Response> updateProject(String id) => _dio.put('/api/projects/$id');
  // NOT_FOUND
  Future<Response> getUnused() => _dio.get('/api/unused');
  // NOT_FOUND (deprecated)
  Future<Response> getAnalytics() => _dio.get('/api/v1/analytics');
}
`
	if err := os.WriteFile(filepath.Join(flutterAPIDir, "app_api.dart"), []byte(flutterDio), 0644); err != nil {
		t.Fatal(err)
	}

	// Flutter direct GitHub calls
	networkDir := filepath.Join(dir, "flutter", "lib", "core", "network")
	if err := os.MkdirAll(networkDir, 0755); err != nil {
		t.Fatal(err)
	}
	remoteSource := `
class IssueRemoteDataSource {
  Future fetchIssues() async {
    return http.get(Uri.parse('https://api.github.com/repos/owner/repo/issues'));
  }
}
`
	if err := os.WriteFile(filepath.Join(networkDir, "issue_remote_data_source.dart"), []byte(remoteSource), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewApiAlignmentService(
		filepath.Join(dir, "angular"), // intentionally missing
		filepath.Join(dir, "flutter"),
		filepath.Join(dir, "platform"),
	)

	report, err := svc.Run()
	if err != nil {
		t.Fatalf("Run() failed: %v", err)
	}

	if len(report.Findings) < 8 {
		t.Errorf("expected at least 8 findings for regression test, got %d: %+v", len(report.Findings), report.Findings)
	}

	// Check all expected categories are present
	categories := make(map[string]int)
	for _, f := range report.Findings {
		categories[f.Category]++
	}

	expectedCategories := []string{"PATH_MISMATCH", "METHOD_MISMATCH", "AUTH_MISMATCH", "NOT_FOUND", "DIRECT_GITHUB_CALL"}
	for _, cat := range expectedCategories {
		if categories[cat] == 0 {
			t.Errorf("expected category %s in findings, got categories: %v", cat, categories)
		}
	}

	// Check DIRECT_GITHUB_CALL specifically
	if categories["DIRECT_GITHUB_CALL"] == 0 {
		t.Error("expected DIRECT_GITHUB_CALL finding for api.github.com call")
	}

	// Verify audited_repos tracking
	if !report.AuditedRepos.Flutter {
		t.Error("expected flutter to be marked as audited")
	}
	if !report.AuditedRepos.Platform {
		t.Error("expected platform to be marked as audited")
	}
	if report.AuditedRepos.Angular {
		t.Error("expected angular to be marked as NOT audited (repo missing)")
	}
}
