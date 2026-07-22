package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// --- Stub implementations ---

type stubScheduler struct {
	queued []QueueEntry
}

func (s *stubScheduler) QueueAdd(entries ...QueueEntry) {
	s.queued = append(s.queued, entries...)
}

type stubExecMgr struct {
	stopped   []string
	cancelled []struct {
		key     string
		timeout time.Duration
	}
	// gracefulResult controls what CancelWithGrace returns for tests.
	gracefulResult bool
}

func (m *stubExecMgr) Stop(key string) {
	m.stopped = append(m.stopped, key)
}

func (m *stubExecMgr) CancelWithGrace(key string, timeout time.Duration) (bool, error) {
	m.cancelled = append(m.cancelled, struct {
		key     string
		timeout time.Duration
	}{key, timeout})
	return m.gracefulResult, nil
}

type stubIssueGetter struct {
	issues map[string]bool // key: "owner/repo#number" → true=found
	err    error
}

func (g *stubIssueGetter) GetIssue(ctx context.Context, owner, repo string, number int) (interface{}, error) {
	if g.err != nil {
		return nil, g.err
	}
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	if g.issues[key] {
		return map[string]interface{}{"number": number}, nil // non-nil = found
	}
	return nil, nil // nil = not found
}

type stubStateSvc struct {
	states map[string]interface{}
}

func (s *stubStateSvc) GetState(key string) interface{} {
	return s.states[key]
}

// --- Tests ---

func TestNew_DefaultTimeout(t *testing.T) {
	e := New(0)
	if e.timeout != DefaultTimeout {
		t.Errorf("expected default timeout %v, got %v", DefaultTimeout, e.timeout)
	}
}

func TestNew_CustomTimeout(t *testing.T) {
	e := New(10 * time.Second)
	if e.timeout != 10*time.Second {
		t.Errorf("expected 10s timeout, got %v", e.timeout)
	}
}

func TestRegister_OverwritesExisting(t *testing.T) {
	e := New(0)
	first := false
	e.Register("test.cmd", func(ctx context.Context, p json.RawMessage) (interface{}, error) {
		first = true
		return nil, nil
	})
	e.Register("test.cmd", func(ctx context.Context, p json.RawMessage) (interface{}, error) {
		return "second", nil
	})

	result, err := e.Execute(context.Background(), Command{Type: "test.cmd"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first {
		t.Error("first handler should have been overwritten")
	}
	if result.Data != "second" {
		t.Errorf("expected 'second', got %v", result.Data)
	}
}

func TestExecute_UnknownType_ReturnsErrorResult(t *testing.T) {
	e := New(0)
	result, err := e.Execute(context.Background(), Command{Type: "unknown.cmd"})
	if err != nil {
		t.Fatalf("Execute should not return Go error for unknown type, got: %v", err)
	}
	if result.Error == "" {
		t.Error("expected non-empty Error field in result")
	}
	want := "unknown command type: unknown.cmd"
	if result.Error != want {
		t.Errorf("expected %q, got %q", want, result.Error)
	}
	if result.Type != "unknown.cmd" {
		t.Errorf("expected Type to be preserved, got %q", result.Type)
	}
}

func TestExecute_DispatchByType(t *testing.T) {
	sched := &stubScheduler{}
	mgr := &stubExecMgr{}
	stateSvc := &stubStateSvc{states: map[string]interface{}{
		"exec-1": map[string]interface{}{"status": "running", "stage": "feature-dev"},
	}}
	configLoaded := false
	configLoad := func(root string) (interface{}, error) {
		configLoaded = true
		return nil, nil
	}

	issueGetter := &stubIssueGetter{issues: map[string]bool{"acme/myrepo#42": true}}
	deps := Deps{
		Scheduler:   sched,
		ExecMgr:     mgr,
		StateSvc:    stateSvc,
		IssueGetter: issueGetter,
		ConfigLoad:  configLoad,
		WorkRoot:    "/workspace",
	}
	e := NewWithHandlers(deps)

	tests := []struct {
		name     string
		cmdType  CommandType
		payload  json.RawMessage
		wantErr  string // empty = success
		validate func(t *testing.T, result *Result)
	}{
		{
			name:    "pipeline.run queues item",
			cmdType: CommandTypePipelineRun,
			payload: json.RawMessage(`{"owner":"acme","repo":"myrepo","issueNumber":42}`),
			validate: func(t *testing.T, result *Result) {
				if result.Error != "" {
					t.Errorf("unexpected error: %s", result.Error)
				}
				if len(sched.queued) != 1 {
					t.Fatalf("expected 1 queued item, got %d", len(sched.queued))
				}
				if sched.queued[0].IssueNumber != 42 {
					t.Errorf("expected issue 42, got %d", sched.queued[0].IssueNumber)
				}
			},
		},
		{
			name:    "pipeline.cancel calls CancelWithGrace and returns ok",
			cmdType: CommandTypePipelineCancel,
			payload: json.RawMessage(`{"executionId":"exec-1"}`),
			validate: func(t *testing.T, result *Result) {
				if result.Error != "" {
					t.Errorf("unexpected error: %s", result.Error)
				}
				if len(mgr.cancelled) != 1 {
					t.Fatalf("expected CancelWithGrace to be called once, got %d", len(mgr.cancelled))
				}
				if mgr.cancelled[0].key != "exec-1" {
					t.Errorf("expected key exec-1, got %q", mgr.cancelled[0].key)
				}
				if mgr.cancelled[0].timeout != cancelGracePeriod {
					t.Errorf("expected timeout %v, got %v", cancelGracePeriod, mgr.cancelled[0].timeout)
				}
				data, ok := result.Data.(map[string]interface{})
				if !ok {
					t.Fatalf("expected map result, got %T", result.Data)
				}
				if data["status"] != "ok" {
					t.Errorf("expected status ok, got %v", data["status"])
				}
				if data["executionId"] != "exec-1" {
					t.Errorf("expected executionId exec-1, got %v", data["executionId"])
				}
				if data["graceful"] != false {
					t.Errorf("expected graceful false (stub default), got %v", data["graceful"])
				}
			},
		},
		{
			name:    "pipeline.status returns state",
			cmdType: CommandTypePipelineStatus,
			payload: json.RawMessage(`{"executionId":"exec-1"}`),
			validate: func(t *testing.T, result *Result) {
				if result.Error != "" {
					t.Errorf("unexpected error: %s", result.Error)
				}
				data, ok := result.Data.(map[string]interface{})
				if !ok {
					t.Fatalf("expected map result, got %T", result.Data)
				}
				if data["executionId"] != "exec-1" {
					t.Errorf("expected executionId exec-1, got %v", data["executionId"])
				}
				// stub returns map[string]string — handler passes through with executionId injected
				if data["stage"] != "feature-dev" {
					t.Errorf("expected stage feature-dev, got %v", data["stage"])
				}
			},
		},
		{
			name:    "pipeline.status returns idle for missing execution",
			cmdType: CommandTypePipelineStatus,
			payload: json.RawMessage(`{"executionId":"missing"}`),
			validate: func(t *testing.T, result *Result) {
				if result.Error != "" {
					t.Errorf("unexpected error: %s", result.Error)
				}
				data, ok := result.Data.(map[string]interface{})
				if !ok {
					t.Fatalf("expected map result, got %T", result.Data)
				}
				if data["status"] != "idle" {
					t.Errorf("expected status idle, got %v", data["status"])
				}
			},
		},
		{
			name:    "config.reload reloads config",
			cmdType: CommandTypeConfigReload,
			payload: json.RawMessage(`{}`),
			validate: func(t *testing.T, result *Result) {
				if result.Error != "" {
					t.Errorf("unexpected error: %s", result.Error)
				}
				if !configLoaded {
					t.Error("expected config loader to be called")
				}
				data, ok := result.Data.(map[string]interface{})
				if !ok {
					t.Fatalf("expected map result, got %T", result.Data)
				}
				if data["reloaded"] != true {
					t.Errorf("expected reloaded:true, got %v", data["reloaded"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := e.Execute(context.Background(), Command{
				Type:    tt.cmdType,
				Payload: tt.payload,
			})
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if tt.wantErr != "" {
				if result.Error != tt.wantErr {
					t.Errorf("expected error %q, got %q", tt.wantErr, result.Error)
				}
				return
			}
			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestExecute_TimeoutEnforced(t *testing.T) {
	e := New(50 * time.Millisecond)

	blocked := make(chan struct{})
	e.Register("slow.cmd", func(ctx context.Context, p json.RawMessage) (interface{}, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-blocked:
			return "done", nil
		}
	})
	defer close(blocked)

	result, err := e.Execute(context.Background(), Command{Type: "slow.cmd"})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected timeout error in result.Error")
	}
	if !errors.Is(context.DeadlineExceeded, context.DeadlineExceeded) {
		// always true — just validate result.Error contains something meaningful
	}
}

func TestExecute_PerCommandTimeoutOverride(t *testing.T) {
	e := New(10 * time.Second) // long default

	e.Register("fast.cancel", func(ctx context.Context, p json.RawMessage) (interface{}, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return "too slow", nil
		}
	})

	result, err := e.Execute(context.Background(), Command{
		Type:    "fast.cancel",
		Timeout: 50 * time.Millisecond, // per-command override
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected per-command timeout to fire and set result.Error")
	}
}

func TestPipelineRunHandler_MissingRepo(t *testing.T) {
	e := NewWithHandlers(Deps{
		Scheduler: &stubScheduler{},
	})
	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"issueNumber":1}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected validation error for missing repo")
	}
}

func TestPipelineRunHandler_MissingIssueNumber(t *testing.T) {
	e := NewWithHandlers(Deps{
		Scheduler: &stubScheduler{},
	})
	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"repo":"acme/myrepo"}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected validation error for missing issueNumber")
	}
}

func TestPipelineCancelHandler_MissingExecutionID(t *testing.T) {
	e := NewWithHandlers(Deps{
		ExecMgr: &stubExecMgr{},
	})
	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineCancel,
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected validation error for missing executionId")
	}
}

func TestPipelineStatusHandler_MissingExecutionID(t *testing.T) {
	e := NewWithHandlers(Deps{
		StateSvc: &stubStateSvc{states: map[string]interface{}{}},
	})
	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineStatus,
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected validation error for missing executionId or issueNumber")
	}
}

func TestPipelineStatusHandler_IssueNumberParam(t *testing.T) {
	stateSvc := &stubStateSvc{states: map[string]interface{}{
		"42": map[string]interface{}{"status": "running", "stage": "feature-dev", "issueNumber": 42},
	}}
	e := NewWithHandlers(Deps{StateSvc: stateSvc})
	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineStatus,
		Payload: json.RawMessage(`{"issueNumber":42}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("unexpected error: %s", result.Error)
	}
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result.Data)
	}
	if data["status"] != "running" {
		t.Errorf("expected status running, got %v", data["status"])
	}
}

func TestPipelineCancelHandler_GracefulTrue(t *testing.T) {
	mgr := &stubExecMgr{gracefulResult: true}
	e := NewWithHandlers(Deps{ExecMgr: mgr})

	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineCancel,
		Payload: json.RawMessage(`{"executionId":"exec-graceful"}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("unexpected result error: %s", result.Error)
	}
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result.Data)
	}
	if data["graceful"] != true {
		t.Errorf("expected graceful true, got %v", data["graceful"])
	}
	if data["executionId"] != "exec-graceful" {
		t.Errorf("expected executionId exec-graceful, got %v", data["executionId"])
	}
}

func TestPipelineCancelHandler_GracefulFalse(t *testing.T) {
	mgr := &stubExecMgr{gracefulResult: false}
	e := NewWithHandlers(Deps{ExecMgr: mgr})

	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineCancel,
		Payload: json.RawMessage(`{"executionId":"exec-forced"}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("unexpected result error: %s", result.Error)
	}
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result.Data)
	}
	if data["graceful"] != false {
		t.Errorf("expected graceful false, got %v", data["graceful"])
	}
}

func TestConfigReloadHandler_LoadError(t *testing.T) {
	e := NewWithHandlers(Deps{
		ConfigLoad: func(root string) (interface{}, error) {
			return nil, errors.New("config file not found")
		},
		WorkRoot: "/workspace",
	})
	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypeConfigReload,
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error result when config load fails")
	}
}

func TestHandler_NilDependencies(t *testing.T) {
	tests := []struct {
		name        string
		deps        Deps
		cmdType     CommandType
		payload     json.RawMessage
		wantErrFrag string
	}{
		{
			name:        "pipeline.run nil scheduler",
			deps:        Deps{Scheduler: nil, ExecMgr: &stubExecMgr{}, StateSvc: &stubStateSvc{states: map[string]interface{}{}}, IssueGetter: &stubIssueGetter{issues: map[string]bool{"o/r#1": true}}, ConfigLoad: func(root string) (interface{}, error) { return nil, nil }},
			cmdType:     CommandTypePipelineRun,
			payload:     json.RawMessage(`{"repo":"r","issueNumber":1}`),
			wantErrFrag: "scheduler not configured",
		},
		{
			name:        "pipeline.cancel nil exec manager",
			deps:        Deps{Scheduler: &stubScheduler{}, ExecMgr: nil, StateSvc: &stubStateSvc{states: map[string]interface{}{}}, ConfigLoad: func(root string) (interface{}, error) { return nil, nil }},
			cmdType:     CommandTypePipelineCancel,
			payload:     json.RawMessage(`{"executionId":"exec-1"}`),
			wantErrFrag: "execution manager not configured",
		},
		{
			name:        "pipeline.status nil state service",
			deps:        Deps{Scheduler: &stubScheduler{}, ExecMgr: &stubExecMgr{}, StateSvc: nil, ConfigLoad: func(root string) (interface{}, error) { return nil, nil }},
			cmdType:     CommandTypePipelineStatus,
			payload:     json.RawMessage(`{"executionId":"exec-1"}`),
			wantErrFrag: "state service not configured",
		},
		{
			name:        "config.reload nil config loader",
			deps:        Deps{Scheduler: &stubScheduler{}, ExecMgr: &stubExecMgr{}, StateSvc: &stubStateSvc{states: map[string]interface{}{}}, ConfigLoad: nil},
			cmdType:     CommandTypeConfigReload,
			payload:     json.RawMessage(`{}`),
			wantErrFrag: "config loader not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewWithHandlers(tt.deps)
			result, err := e.Execute(context.Background(), Command{
				Type:    tt.cmdType,
				Payload: tt.payload,
			})
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if result.Error == "" {
				t.Errorf("expected error containing %q, got empty result.Error", tt.wantErrFrag)
				return
			}
			if !containsStr(result.Error, tt.wantErrFrag) {
				t.Errorf("result.Error = %q, want to contain %q", result.Error, tt.wantErrFrag)
			}
		})
	}
}

func TestHandler_InvalidJSONPayload(t *testing.T) {
	deps := Deps{
		Scheduler: &stubScheduler{},
		ExecMgr:   &stubExecMgr{},
		StateSvc:  &stubStateSvc{states: map[string]interface{}{}},
		ConfigLoad: func(root string) (interface{}, error) {
			return nil, nil
		},
	}
	e := NewWithHandlers(deps)

	tests := []struct {
		name    string
		cmdType CommandType
	}{
		{"pipeline.run invalid payload", CommandTypePipelineRun},
		{"pipeline.cancel invalid payload", CommandTypePipelineCancel},
		{"pipeline.status invalid payload", CommandTypePipelineStatus},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := e.Execute(context.Background(), Command{
				Type:    tt.cmdType,
				Payload: json.RawMessage(`{bad json}`),
			})
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if result.Error == "" {
				t.Error("expected error for invalid JSON payload, got empty result.Error")
				return
			}
			if !containsStr(result.Error, "invalid payload") {
				t.Errorf("result.Error = %q, want to contain 'invalid payload'", result.Error)
			}
		})
	}
}

func TestPipelineRunHandler_IssueValidation_Found(t *testing.T) {
	sched := &stubScheduler{}
	getter := &stubIssueGetter{issues: map[string]bool{"acme/myrepo#42": true}}
	e := NewWithHandlers(Deps{Scheduler: sched, IssueGetter: getter})

	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"owner":"acme","repo":"myrepo","issueNumber":42}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("unexpected error: %s", result.Error)
	}
	if len(sched.queued) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(sched.queued))
	}
	if sched.queued[0].IssueNumber != 42 {
		t.Errorf("expected issue 42, got %d", sched.queued[0].IssueNumber)
	}
}

func TestPipelineRunHandler_IssueValidation_NotFound(t *testing.T) {
	sched := &stubScheduler{}
	getter := &stubIssueGetter{issues: map[string]bool{}} // empty = not found
	e := NewWithHandlers(Deps{Scheduler: sched, IssueGetter: getter})

	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"owner":"acme","repo":"myrepo","issueNumber":99}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for issue not found")
	}
	if !containsStr(result.Error, "not found") {
		t.Errorf("expected 'not found' in error, got %q", result.Error)
	}
	if len(sched.queued) != 0 {
		t.Errorf("expected no items queued, got %d", len(sched.queued))
	}
}

func TestPipelineRunHandler_IssueValidation_GetterError(t *testing.T) {
	sched := &stubScheduler{}
	getter := &stubIssueGetter{err: errors.New("API rate limit exceeded")}
	e := NewWithHandlers(Deps{Scheduler: sched, IssueGetter: getter})

	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"owner":"acme","repo":"myrepo","issueNumber":1}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error from getter failure")
	}
	if !containsStr(result.Error, "failed to verify issue") {
		t.Errorf("expected 'failed to verify issue' in error, got %q", result.Error)
	}
	if !containsStr(result.Error, "API rate limit exceeded") {
		t.Errorf("expected wrapped cause in error, got %q", result.Error)
	}
}

func TestPipelineRunHandler_IssueValidation_NoOwner(t *testing.T) {
	sched := &stubScheduler{}
	getter := &stubIssueGetter{issues: map[string]bool{}} // would not find anything
	e := NewWithHandlers(Deps{Scheduler: sched, IssueGetter: getter})

	// Bare repo name with no owner — validation should be skipped
	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"repo":"myrepo","issueNumber":10}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("expected no error (skip validation), got %q", result.Error)
	}
	if len(sched.queued) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(sched.queued))
	}
}

func TestPipelineRunHandler_IssueValidation_NilGetter(t *testing.T) {
	sched := &stubScheduler{}
	e := NewWithHandlers(Deps{Scheduler: sched}) // no IssueGetter

	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"owner":"acme","repo":"myrepo","issueNumber":1}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("expected no error (nil getter skips validation), got %q", result.Error)
	}
	if len(sched.queued) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(sched.queued))
	}
}

func TestPipelineRunHandler_IssueValidation_OwnerRepoFormat(t *testing.T) {
	sched := &stubScheduler{}
	// Getter knows about "acme/myrepo#5" — repo field is "acme/myrepo", owner is empty
	getter := &stubIssueGetter{issues: map[string]bool{"acme/myrepo#5": true}}
	e := NewWithHandlers(Deps{Scheduler: sched, IssueGetter: getter})

	result, err := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"repo":"acme/myrepo","issueNumber":5}`),
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("expected no error, got %q", result.Error)
	}
	if len(sched.queued) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(sched.queued))
	}
}

func TestCommandExecutor_UpdateDeps(t *testing.T) {
	// Start with nil scheduler — pipeline.run should fail
	e := NewWithHandlers(Deps{
		StateSvc: &stubStateSvc{states: map[string]interface{}{}},
	})
	result, _ := e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"owner":"acme","repo":"myrepo","issueNumber":1}`),
	})
	if !containsStr(result.Error, "scheduler not configured") {
		t.Fatalf("expected scheduler not configured before UpdateDeps, got %q", result.Error)
	}

	// UpdateDeps with a real scheduler — pipeline.run should succeed
	sched := &stubScheduler{}
	getter := &stubIssueGetter{issues: map[string]bool{"acme/myrepo#1": true}}
	e.UpdateDeps(Deps{
		Scheduler:   sched,
		IssueGetter: getter,
		StateSvc:    &stubStateSvc{states: map[string]interface{}{}},
	})

	result, _ = e.Execute(context.Background(), Command{
		Type:    CommandTypePipelineRun,
		Payload: json.RawMessage(`{"owner":"acme","repo":"myrepo","issueNumber":1}`),
	})
	if result.Error != "" {
		t.Errorf("expected success after UpdateDeps, got %q", result.Error)
	}
	if len(sched.queued) != 1 {
		t.Errorf("expected 1 queued item, got %d", len(sched.queued))
	}
}

func TestSplitOwnerRepoFromParams(t *testing.T) {
	tests := []struct {
		owner, repo         string
		wantOwner, wantRepo string
	}{
		{"acme", "myrepo", "acme", "myrepo"},
		{"", "acme/myrepo", "acme", "myrepo"},
		{"", "myrepo", "", "myrepo"},
		{"explicit", "org/repo", "explicit", "org/repo"}, // explicit owner wins
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("owner=%q,repo=%q", tt.owner, tt.repo), func(t *testing.T) {
			gotOwner, gotRepo := splitOwnerRepoFromParams(tt.owner, tt.repo)
			if gotOwner != tt.wantOwner || gotRepo != tt.wantRepo {
				t.Errorf("splitOwnerRepoFromParams(%q, %q) = (%q, %q), want (%q, %q)",
					tt.owner, tt.repo, gotOwner, gotRepo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

// containsStr is a simple string-contains helper for test assertions.
func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
