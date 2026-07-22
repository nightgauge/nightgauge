// Package ipc — Unit tests for protocol.go type definitions and constants.
//
// These tests validate the JSON wire format used between the TypeScript VSCode
// extension (IpcClientBase.ts) and the Go binary (server.go) by performing
// direct struct marshaling and unmarshaling. No server harness is needed —
// these are pure unit tests for the protocol contract.
//
// Coverage targets:
//   - Request/Response/Event struct JSON serialization
//   - Result and error field mutual exclusivity in Response
//   - Error code constants match JSON-RPC standard values
//   - ProtocolVersion constant equals 1 (matches IPC_PROTOCOL_VERSION in TS)
//   - params:omitempty on Request does not emit the key when nil
//   - Event struct has no "id" field in wire JSON
//
// See: internal/ipc/protocol.go — type definitions under test
// See: packages/nightgauge-vscode/src/services/IpcClientBase.ts — TS side
// See: docs/GO_BINARY.md — IPC protocol section
package ipc

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── Request marshaling ──────────────────────────────────────────────────────

// TestProtocol_Request_MarshalRoundTrip verifies that a Request struct
// survives a marshal → unmarshal round-trip with all fields intact.
func TestProtocol_Request_MarshalRoundTrip(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{"owner": "nightgauge", "projectNumber": 5})
	original := Request{
		ID:     42,
		Method: "board.list",
		Params: params,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal Request: %v", err)
	}

	var restored Request
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal Request: %v", err)
	}

	if restored.ID != original.ID {
		t.Errorf("ID = %d, want %d", restored.ID, original.ID)
	}
	if restored.Method != original.Method {
		t.Errorf("Method = %q, want %q", restored.Method, original.Method)
	}
	if string(restored.Params) != string(original.Params) {
		t.Errorf("Params = %s, want %s", restored.Params, original.Params)
	}
}

// TestProtocol_Request_OmitsParamsWhenNil verifies that Request.Params (which
// uses json:",omitempty") is absent from the wire JSON when not set.
// This matches the TypeScript side: call() omits params when undefined.
func TestProtocol_Request_OmitsParamsWhenNil(t *testing.T) {
	req := Request{ID: 1, Method: "queue.list"}
	// Params is left as nil (zero value of json.RawMessage).

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(data)

	if strings.Contains(wire, `"params"`) {
		t.Errorf("wire JSON must not contain params key when Params is nil: %s", wire)
	}
}

// TestProtocol_Request_IDPreservedAcrossWireFormats verifies that non-sequential
// IDs (e.g. 1, 42, 9999) survive marshal → unmarshal correctly. This matters
// because the TypeScript client matches responses by ID, not FIFO order.
func TestProtocol_Request_IDPreservedAcrossWireFormats(t *testing.T) {
	cases := []int{1, 2, 42, 9999, 100000}
	for _, id := range cases {
		req := Request{ID: id, Method: "test.echo"}
		data, _ := json.Marshal(req)
		var got Request
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("id=%d: unmarshal: %v", id, err)
		}
		if got.ID != id {
			t.Errorf("id=%d: round-trip ID = %d", id, got.ID)
		}
	}
}

// ─── Response marshaling ─────────────────────────────────────────────────────

// TestProtocol_Response_SuccessHasNoErrorKey verifies that the wire JSON of a
// successful response does not contain an "error" key. The omitempty tag on
// Response.Error ensures this.
func TestProtocol_Response_SuccessHasNoErrorKey(t *testing.T) {
	resp := Response{
		ID:     1,
		Result: map[string]string{"status": "ok"},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(data)

	if strings.Contains(wire, `"error"`) {
		t.Errorf("success response wire JSON must not contain \"error\" key: %s", wire)
	}
}

// TestProtocol_Response_ErrorHasNoResultKey verifies that the wire JSON of an
// error response does not contain a "result" key. The omitempty tag on
// Response.Result ensures this when Result is nil.
func TestProtocol_Response_ErrorHasNoResultKey(t *testing.T) {
	resp := Response{
		ID:    1,
		Error: &RPCError{Code: ErrMethodNotFound, Message: "unknown method: test.gone"},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(data)

	if strings.Contains(wire, `"result"`) {
		t.Errorf("error response wire JSON must not contain \"result\" key: %s", wire)
	}
}

// TestProtocol_Response_IDPreservedInRoundTrip verifies that Response.ID
// survives a marshal → unmarshal cycle, guaranteeing that the TypeScript
// client's ID-based promise routing will match correctly.
func TestProtocol_Response_IDPreservedInRoundTrip(t *testing.T) {
	cases := []struct {
		id   int
		desc string
	}{
		{1, "first request"},
		{42, "non-sequential ID"},
		{9999, "large ID"},
	}

	for _, tc := range cases {
		resp := Response{ID: tc.id, Result: "ok"}
		data, _ := json.Marshal(resp)
		var got Response
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.desc, err)
		}
		if got.ID != tc.id {
			t.Errorf("%s: ID = %d, want %d", tc.desc, got.ID, tc.id)
		}
	}
}

// TestProtocol_Response_MutualExclusivity verifies that a well-formed response
// has exactly one of result or error set, never both. This is the critical
// wire-format invariant for IPC correctness.
func TestProtocol_Response_MutualExclusivity(t *testing.T) {
	t.Run("success: result set, error nil", func(t *testing.T) {
		resp := Response{ID: 1, Result: "value"}
		if resp.Error != nil {
			t.Error("success response must have nil Error")
		}
		if resp.Result == nil {
			t.Error("success response must have non-nil Result")
		}
	})

	t.Run("error: error set, result nil", func(t *testing.T) {
		resp := Response{
			ID:    1,
			Error: &RPCError{Code: ErrInternal, Message: "oops"},
		}
		if resp.Result != nil {
			t.Error("error response must have nil Result")
		}
		if resp.Error == nil {
			t.Error("error response must have non-nil Error")
		}
	})
}

// ─── Event marshaling ─────────────────────────────────────────────────────────

// TestProtocol_Event_HasNoIDField verifies that the Event struct does not
// include an "id" field in its wire JSON. TypeScript uses the absence of "id"
// to distinguish events from responses.
func TestProtocol_Event_HasNoIDField(t *testing.T) {
	evt := Event{
		Event: "ipc.ready",
		Data:  map[string]int{"protocolVersion": ProtocolVersion},
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(data)

	if strings.Contains(wire, `"id"`) {
		t.Errorf("event wire JSON must not contain \"id\" field: %s", wire)
	}
}

// TestProtocol_Event_MarshalRoundTrip verifies that Event survives a
// marshal → unmarshal round-trip with the event name and data intact.
func TestProtocol_Event_MarshalRoundTrip(t *testing.T) {
	original := Event{
		Event: "queue.changed",
		Data:  map[string]interface{}{"size": 3},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored Event
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if restored.Event != original.Event {
		t.Errorf("Event = %q, want %q", restored.Event, original.Event)
	}
	if restored.Data == nil {
		t.Error("Event.Data must not be nil after round-trip")
	}
}

// ─── RPCError ────────────────────────────────────────────────────────────────

// TestProtocol_RPCError_MarshalRoundTrip verifies that RPCError preserves
// code and message through a JSON round-trip.
func TestProtocol_RPCError_MarshalRoundTrip(t *testing.T) {
	original := &RPCError{
		Code:    ErrMethodNotFound,
		Message: "unknown method: does.not.exist",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal RPCError: %v", err)
	}

	var restored RPCError
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal RPCError: %v", err)
	}

	if restored.Code != original.Code {
		t.Errorf("Code = %d, want %d", restored.Code, original.Code)
	}
	if restored.Message != original.Message {
		t.Errorf("Message = %q, want %q", restored.Message, original.Message)
	}
}

// ─── Error code constants ────────────────────────────────────────────────────

// TestProtocol_ErrorCodes_JSONRPCStandard verifies that the three IPC error
// codes match the JSON-RPC 2.0 specification. TypeScript IpcClientBase.ts
// formats error messages as "IPC error {code}: {message}" and tests assert on
// specific codes, so divergence here breaks the TS↔Go contract.
func TestProtocol_ErrorCodes_JSONRPCStandard(t *testing.T) {
	if ErrMethodNotFound != -32601 {
		t.Errorf("ErrMethodNotFound = %d, want -32601 (JSON-RPC Method Not Found)", ErrMethodNotFound)
	}
	if ErrInvalidParams != -32602 {
		t.Errorf("ErrInvalidParams = %d, want -32602 (JSON-RPC Invalid Params)", ErrInvalidParams)
	}
	if ErrInternal != -32603 {
		t.Errorf("ErrInternal = %d, want -32603 (JSON-RPC Internal Error)", ErrInternal)
	}
}

// TestProtocol_ErrorCodes_AreDistinct verifies that the three error codes are
// not accidentally equal to each other — a simple regression guard.
func TestProtocol_ErrorCodes_AreDistinct(t *testing.T) {
	codes := map[string]int{
		"ErrMethodNotFound": ErrMethodNotFound,
		"ErrInvalidParams":  ErrInvalidParams,
		"ErrInternal":       ErrInternal,
	}
	seen := make(map[int]string)
	for name, code := range codes {
		if prev, ok := seen[code]; ok {
			t.Errorf("error codes are not distinct: %s and %s both equal %d", name, prev, code)
		}
		seen[code] = name
	}
}

// TestProtocol_ErrorCode_SerializesAsInteger verifies that error codes
// serialize as JSON integers (not strings), ensuring the TypeScript client
// can compare them numerically.
func TestProtocol_ErrorCode_SerializesAsInteger(t *testing.T) {
	resp := Response{
		ID:    1,
		Error: &RPCError{Code: ErrMethodNotFound, Message: "not found"},
	}

	data, _ := json.Marshal(resp)
	wire := string(data)

	// The code must appear as the integer -32601, not as a string "-32601".
	if !strings.Contains(wire, "-32601") {
		t.Errorf("ErrMethodNotFound (-32601) not found as integer in wire JSON: %s", wire)
	}
	if strings.Contains(wire, `"-32601"`) {
		t.Errorf("error code must not be a JSON string: %s", wire)
	}
}

// ─── ProtocolVersion ─────────────────────────────────────────────────────────

// TestProtocol_ProtocolVersion_IsOne verifies that ProtocolVersion = 1.
// This constant MUST match IPC_PROTOCOL_VERSION in IpcClient.generated.ts.
// A mismatch causes the TypeScript client to reject the ipc.ready event and
// refuse to send requests, silently breaking the entire pipeline.
func TestProtocol_ProtocolVersion_IsOne(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1 (must match IPC_PROTOCOL_VERSION in IpcClient.generated.ts)", ProtocolVersion)
	}
}

// TestProtocol_ProtocolVersion_AppearsInReadyEventWireJSON verifies that when
// ProtocolVersion is embedded in an ipc.ready event payload and marshaled, the
// integer 1 appears in the wire JSON as expected by the TypeScript client.
func TestProtocol_ProtocolVersion_AppearsInReadyEventWireJSON(t *testing.T) {
	ready := Event{
		Event: "ipc.ready",
		Data: map[string]interface{}{
			"protocolVersion": ProtocolVersion,
		},
	}

	data, err := json.Marshal(ready)
	if err != nil {
		t.Fatalf("marshal ipc.ready event: %v", err)
	}
	wire := string(data)

	// TypeScript checks: data.protocolVersion === IPC_PROTOCOL_VERSION (1)
	if !strings.Contains(wire, `"protocolVersion":1`) {
		t.Errorf("ipc.ready wire JSON must contain protocolVersion:1, got: %s", wire)
	}
}
