package spike

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParse_ValidYAMLBlock_ProducesNRecommendations(t *testing.T) {
	art, err := ParseArtifact("testdata/valid-recommendations.md")
	if err != nil {
		t.Fatalf("ParseArtifact: %v", err)
	}
	if art.Spike != 4042 {
		t.Errorf("spike = %d, want 4042", art.Spike)
	}
	if len(art.Recommendations) != 3 {
		t.Fatalf("recommendations count = %d, want 3", len(art.Recommendations))
	}
	if art.Recommendations[0].ID != "alpha" || art.Recommendations[0].Action != "adopt" {
		t.Errorf("recommendation[0] = %+v", art.Recommendations[0])
	}
	if art.Recommendations[2].Action != "skip" {
		t.Errorf("recommendation[2].Action = %q, want skip", art.Recommendations[2].Action)
	}
}

func TestParse_MalformedYAML_FailsFast(t *testing.T) {
	_, err := ParseArtifact("testdata/malformed-yaml.md")
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
	if !strings.Contains(err.Error(), "parse recommendations YAML") {
		t.Errorf("error = %q, want contains 'parse recommendations YAML'", err.Error())
	}
}

func TestParse_MissingRecommendationsBlock_FailsFast(t *testing.T) {
	_, err := ParseArtifact("testdata/missing-block.md")
	if err == nil {
		t.Fatal("expected error for missing recommendations block, got nil")
	}
	if !strings.Contains(err.Error(), "no fenced") {
		t.Errorf("error = %q, want contains 'no fenced'", err.Error())
	}
}

func TestParse_RejectsUnknownTopLevelField(t *testing.T) {
	src := []byte("```yaml recommendations\nspike: 1\nrecommendations: []\nbogus: true\n```\n")
	_, err := ParseArtifactBytes(src)
	if err == nil || !strings.Contains(err.Error(), "unknown top-level field") {
		t.Errorf("expected unknown top-level field error, got %v", err)
	}
}

func TestParse_RejectsUnknownRecommendationField(t *testing.T) {
	src := []byte("```yaml recommendations\nspike: 1\nrecommendations:\n  - id: a\n    action: adopt\n    title: t\n    type: feature\n    priority: high\n    size: S\n    bogus: true\n```\n")
	_, err := ParseArtifactBytes(src)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("expected unknown field error, got %v", err)
	}
}

func TestValidate_DuplicateID_Rejected(t *testing.T) {
	art := &SpikeArtifact{
		Spike: 1,
		Recommendations: []Recommendation{
			validRec("dupe"),
			validRec("dupe"),
		},
	}
	err := ValidateSchema(art)
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Errorf("expected duplicate id error, got %v", err)
	}
}

func TestValidate_UnknownAction_Rejected(t *testing.T) {
	r := validRec("alpha")
	r.Action = "bogus"
	err := ValidateSchema(&SpikeArtifact{Spike: 1, Recommendations: []Recommendation{r}})
	if err == nil || !strings.Contains(err.Error(), "action must be") {
		t.Errorf("expected action enum error, got %v", err)
	}
}

func TestValidate_UnknownType_Rejected(t *testing.T) {
	r := validRec("alpha")
	r.Type = "bogus"
	err := ValidateSchema(&SpikeArtifact{Spike: 1, Recommendations: []Recommendation{r}})
	if err == nil || !strings.Contains(err.Error(), "type must be") {
		t.Errorf("expected type enum error, got %v", err)
	}
}

func TestValidate_UnknownPriority_Rejected(t *testing.T) {
	r := validRec("alpha")
	r.Priority = "P0"
	err := ValidateSchema(&SpikeArtifact{Spike: 1, Recommendations: []Recommendation{r}})
	if err == nil || !strings.Contains(err.Error(), "priority must be") {
		t.Errorf("expected priority enum error, got %v", err)
	}
}

func TestValidate_UnknownSize_Rejected(t *testing.T) {
	r := validRec("alpha")
	r.Size = "huge"
	err := ValidateSchema(&SpikeArtifact{Spike: 1, Recommendations: []Recommendation{r}})
	if err == nil || !strings.Contains(err.Error(), "size must be") {
		t.Errorf("expected size enum error, got %v", err)
	}
}

func TestValidate_DependsOnUnknownID_Rejected(t *testing.T) {
	r := validRec("alpha")
	r.DependsOn = []string{"missing"}
	err := ValidateSchema(&SpikeArtifact{Spike: 1, Recommendations: []Recommendation{r}})
	if err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Errorf("expected unknown id error, got %v", err)
	}
}

func TestValidate_DependsOnCycle_Rejected(t *testing.T) {
	a := validRec("a")
	a.DependsOn = []string{"b"}
	b := validRec("b")
	b.DependsOn = []string{"a"}
	err := ValidateSchema(&SpikeArtifact{Spike: 1, Recommendations: []Recommendation{a, b}})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got %v", err)
	}
}

func TestValidate_NonKebabID_Rejected(t *testing.T) {
	r := validRec("AlphaCamel")
	err := ValidateSchema(&SpikeArtifact{Spike: 1, Recommendations: []Recommendation{r}})
	if err == nil || !strings.Contains(err.Error(), "kebab-case") {
		t.Errorf("expected kebab-case error, got %v", err)
	}
}

// --- Materialize tests ---

// fakeMaterializer is a deterministic in-memory Materializer for tests.
type fakeMaterializer struct {
	existing       map[string]int   // id → existing issue number
	created        []Recommendation // recorded create calls
	createdNums    map[string]int   // id → assigned issue number
	blockedByEdges []BlockedByEdge  // recorded blockedBy calls
	nextNumber     int
	createErr      error
	lookupErr      error
}

func newFake() *fakeMaterializer {
	return &fakeMaterializer{
		existing:    map[string]int{},
		createdNums: map[string]int{},
		nextNumber:  1000,
	}
}

func (f *fakeMaterializer) FindExistingByID(_ context.Context, _ int, id string) (int, string, error) {
	if f.lookupErr != nil {
		return 0, "", f.lookupErr
	}
	if num, ok := f.existing[id]; ok {
		return num, "", nil
	}
	return 0, "", nil
}

func (f *fakeMaterializer) CreateIssue(_ context.Context, _ int, rec Recommendation, _ string) (int, string, error) {
	if f.createErr != nil {
		return 0, "", f.createErr
	}
	f.nextNumber++
	f.created = append(f.created, rec)
	f.createdNums[rec.ID] = f.nextNumber
	return f.nextNumber, "", nil
}

func (f *fakeMaterializer) AddBlockedByByNumber(_ context.Context, blockedNumber, blockerNumber int) error {
	// Reverse-lookup the IDs from createdNums for clearer assertions.
	var blockedID, blockerID string
	for id, num := range f.createdNums {
		if num == blockedNumber {
			blockedID = id
		}
		if num == blockerNumber {
			blockerID = id
		}
	}
	for id, num := range f.existing {
		if num == blockedNumber && blockedID == "" {
			blockedID = id
		}
		if num == blockerNumber && blockerID == "" {
			blockerID = id
		}
	}
	f.blockedByEdges = append(f.blockedByEdges, BlockedByEdge{BlockedID: blockedID, BlockerID: blockerID})
	return nil
}

func TestMaterialize_Idempotent_NoDuplicates(t *testing.T) {
	art, err := ParseArtifact("testdata/valid-recommendations.md")
	if err != nil {
		t.Fatalf("ParseArtifact: %v", err)
	}
	f := newFake()
	f.existing["alpha"] = 555 // already-materialized

	res, err := Materialize(context.Background(), art, "owner/repo", f, false)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// alpha existed → not created. beta is defer (still creates). gamma is skip.
	if len(f.created) != 1 {
		t.Errorf("created count = %d, want 1 (only beta)", len(f.created))
	}
	if len(f.created) > 0 && f.created[0].ID != "beta" {
		t.Errorf("created[0].ID = %q, want beta", f.created[0].ID)
	}

	// Result must include all 3 entries with correct flags.
	if len(res.Issues) != 3 {
		t.Fatalf("result issues = %d, want 3", len(res.Issues))
	}
	if !res.Issues[0].AlreadyExists {
		t.Errorf("alpha must be marked AlreadyExists")
	}
	if res.Issues[0].IssueNumber != 555 {
		t.Errorf("alpha IssueNumber = %d, want 555", res.Issues[0].IssueNumber)
	}
	if !res.Issues[2].Skipped {
		t.Errorf("gamma must be marked Skipped")
	}
}

func TestMaterialize_BlockedByChainsCreated(t *testing.T) {
	art, err := ParseArtifact("testdata/with-blocked-by.md")
	if err != nil {
		t.Fatalf("ParseArtifact: %v", err)
	}
	f := newFake()
	res, err := Materialize(context.Background(), art, "owner/repo", f, false)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(f.created) != 2 {
		t.Fatalf("created count = %d, want 2", len(f.created))
	}
	// Foundation must be created BEFORE builds-on-foundation due to topo order.
	if f.created[0].ID != "foundation" {
		t.Errorf("created[0].ID = %q, want foundation", f.created[0].ID)
	}
	if f.created[1].ID != "builds-on-foundation" {
		t.Errorf("created[1].ID = %q, want builds-on-foundation", f.created[1].ID)
	}
	if len(f.blockedByEdges) != 1 {
		t.Fatalf("blockedBy edges = %d, want 1", len(f.blockedByEdges))
	}
	edge := f.blockedByEdges[0]
	if edge.BlockedID != "builds-on-foundation" || edge.BlockerID != "foundation" {
		t.Errorf("edge = %+v, want {builds-on-foundation, foundation}", edge)
	}
	if len(res.BlockedBy) != 1 {
		t.Errorf("result.BlockedBy len = %d, want 1", len(res.BlockedBy))
	}
}

func TestMaterialize_DryRun_NoMutations(t *testing.T) {
	art, err := ParseArtifact("testdata/with-blocked-by.md")
	if err != nil {
		t.Fatalf("ParseArtifact: %v", err)
	}
	f := newFake()
	res, err := Materialize(context.Background(), art, "owner/repo", f, true)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(f.created) != 0 {
		t.Errorf("dry-run created %d issues, want 0", len(f.created))
	}
	if len(f.blockedByEdges) != 0 {
		t.Errorf("dry-run added %d blockedBy edges, want 0", len(f.blockedByEdges))
	}
	// Result must still record the planned blockedBy edges so callers can
	// preview the chain.
	if len(res.BlockedBy) != 1 {
		t.Errorf("result.BlockedBy len = %d, want 1", len(res.BlockedBy))
	}
	for _, mi := range res.Issues {
		if !mi.DryRun {
			t.Errorf("result issue %q missing DryRun flag", mi.ID)
		}
	}
}

func TestMaterialize_LookupError_PropagatesAndAborts(t *testing.T) {
	art, err := ParseArtifact("testdata/with-blocked-by.md")
	if err != nil {
		t.Fatalf("ParseArtifact: %v", err)
	}
	f := newFake()
	f.lookupErr = errors.New("boom")
	_, err = Materialize(context.Background(), art, "owner/repo", f, false)
	if err == nil || !strings.Contains(err.Error(), "idempotency lookup") {
		t.Errorf("expected idempotency lookup error, got %v", err)
	}
	if len(f.created) != 0 {
		t.Errorf("error path created %d issues, want 0", len(f.created))
	}
}

func TestBodyFor_IncludesMarkerAndPartOf(t *testing.T) {
	body := BodyFor(42, Recommendation{ID: "alpha", Body: "Hello"})
	if !strings.Contains(body, "<!-- spike-recommendation: id=alpha spike=#42 -->") {
		t.Errorf("body missing marker: %q", body)
	}
	if !strings.Contains(body, "Part of #42") {
		t.Errorf("body missing 'Part of #42': %q", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Errorf("body missing supplied content: %q", body)
	}
}

func TestBodyFor_GeneratesStubWhenBodyEmpty(t *testing.T) {
	body := BodyFor(42, Recommendation{ID: "alpha"})
	if !strings.Contains(body, "Materialized from spike #42") {
		t.Errorf("expected generated stub, got %q", body)
	}
}

// validRec returns a recommendation with all required fields populated.
func validRec(id string) Recommendation {
	return Recommendation{
		ID:       id,
		Action:   "adopt",
		Title:    "Title for " + id,
		Type:     "feature",
		Priority: "high",
		Size:     "M",
	}
}
