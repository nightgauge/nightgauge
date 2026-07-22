/**
 * Tests for TelemetrySettingsMessageHandler (#3327).
 *
 * The webview HTML is rendered as a string and only re-hydrated client-side,
 * so this suite focuses on the message-handler protocol — the unit that
 * stitches webview messages to TelemetryConsentService calls.
 */

import { describe, it, expect, vi } from "vitest";
import { TelemetrySettingsMessageHandler } from "../../../src/views/telemetry/TelemetrySettingsMessageHandler";

function makeCallbacks() {
  return {
    onGetState: vi.fn(),
    onSetEnabled: vi.fn(),
    onToggleStream: vi.fn(),
    onSetUploadInterval: vi.fn(),
    onOpenPrivacyDoc: vi.fn(),
  };
}

describe("TelemetrySettingsMessageHandler", () => {
  it("getState → onGetState invoked", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({ type: "getState" });
    expect(cb.onGetState).toHaveBeenCalledTimes(1);
  });

  it("setEnabled with boolean → onSetEnabled(value)", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({ type: "setEnabled", value: true });
    expect(cb.onSetEnabled).toHaveBeenCalledWith(true);
  });

  it("setEnabled with non-boolean is ignored", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({ type: "setEnabled", value: "yes" });
    expect(cb.onSetEnabled).not.toHaveBeenCalled();
  });

  it("toggleStream → onToggleStream(stream, enabled)", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({
      type: "toggleStream",
      stream: "pipeline-run",
      enabled: false,
    });
    expect(cb.onToggleStream).toHaveBeenCalledWith("pipeline-run", false);
  });

  it("setUploadInterval with number → onSetUploadInterval(minutes)", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({ type: "setUploadInterval", minutes: 30 });
    expect(cb.onSetUploadInterval).toHaveBeenCalledWith(30);
  });

  it("setUploadInterval with non-number is ignored", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({ type: "setUploadInterval", minutes: "soon" });
    expect(cb.onSetUploadInterval).not.toHaveBeenCalled();
  });

  it("openPrivacyDoc → onOpenPrivacyDoc invoked", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({ type: "openPrivacyDoc" });
    expect(cb.onOpenPrivacyDoc).toHaveBeenCalledTimes(1);
  });

  it("unknown type is silently ignored", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({ type: "garbage" });
    expect(cb.onGetState).not.toHaveBeenCalled();
    expect(cb.onSetEnabled).not.toHaveBeenCalled();
  });

  it("malformed message (no type) is ignored", async () => {
    const cb = makeCallbacks();
    const h = new TelemetrySettingsMessageHandler(cb);
    await h.handleMessage({} as any);
    await h.handleMessage(null as any);
    await h.handleMessage("string" as any);
    expect(cb.onGetState).not.toHaveBeenCalled();
  });
});
