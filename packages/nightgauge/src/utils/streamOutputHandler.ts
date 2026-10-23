/**
 * Stream Output Handler - Accumulates content_block_delta fragments
 *
 * Claude's stream-json format emits text in small fragments via
 * content_block_delta messages.  Each fragment is typically a word or
 * partial line — too small for reliable content-type detection (code
 * vs text).  This handler accumulates fragments and flushes them as a
 * single block when content_block_stop arrives, so appendLine() gets
 * the complete text for accurate classification.
 *
 * Also detects phase markers emitted by skills and fires an optional
 * callback so the pipeline tree view can show phase progress.
 *
 * Used by pickupIssue command handler that processes
 * HeadlessOrchestrator onStdout/onStderr callbacks.
 *
 * @see Issue #892 - Accumulate streaming deltas before rendering
 * @see Issue #1027 - Skills emit structured phase markers
 */

import type { PipelineStage } from "@nightgauge/sdk";
import { parsePhaseMarkers, type ParsedPhaseMarker } from "@nightgauge/sdk";
import type { OutputWindow } from "../views/outputWindow/OutputWindow";
import { isStreamJsonEnvelope, isEnvelopeFragment } from "./streamJsonFilter";

/**
 * Callback invoked when a phase marker is detected in the stream.
 *
 * @param stage - The pipeline stage the output belongs to
 * @param marker - The parsed phase marker data
 */
export type PhaseDetectedCallback = (stage: PipelineStage, marker: ParsedPhaseMarker) => void;

/**
 * Options for creating a stream output handler.
 */
export interface StreamOutputHandlerOptions {
  /** Called when a `<!-- phase:start ... -->` marker is detected in stdout */
  onPhaseDetected?: PhaseDetectedCallback;
}

/**
 * Scan a text block for phase markers and invoke the callback if found.
 * Phase markers are HTML comments that span a single line.
 *
 * @returns true if a phase marker was found (so caller can suppress it from output)
 */
function detectPhaseMarker(
  text: string,
  stage: PipelineStage,
  callback?: PhaseDetectedCallback
): boolean {
  if (!callback) {
    if (text.includes("phase:start")) {
      console.warn(
        `[streamOutputHandler] phase:start in text but onPhaseDetected callback not wired for stage=${stage}`
      );
    }
    return false;
  }

  const markers = parsePhaseMarkers(text);
  if (markers.length === 0) return false;
  for (const marker of markers) {
    callback(stage, marker);
  }
  return true;
}

/**
 * Scan a user message envelope for tool_result content containing phase markers.
 * Called BEFORE the line is filtered as display noise, so printf-path phase
 * markers are detected even though the envelope itself is never rendered. (#3748)
 */
function tryDetectToolResultPhaseMarkers(
  line: string,
  stage: PipelineStage,
  callback?: PhaseDetectedCallback
): void {
  if (!callback) return;
  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(line.trim()) as Record<string, unknown>;
  } catch {
    return;
  }
  const message = parsed.message as { content?: unknown[] } | undefined;
  if (!Array.isArray(message?.content)) return;
  for (const block of message.content) {
    const b = block as { type?: string; content?: unknown };
    if (b.type !== "tool_result") continue;
    let resultText = "";
    if (typeof b.content === "string") {
      resultText = b.content;
    } else if (Array.isArray(b.content)) {
      resultText = (b.content as { type?: string; text?: string }[])
        .filter((c) => c.type === "text" && typeof c.text === "string")
        .map((c) => c.text as string)
        .join("");
    }
    if (!resultText) continue;
    const markers = parsePhaseMarkers(resultText);
    for (const marker of markers) {
      console.log(
        `[streamOutputHandler] phase marker delivered via tool_result: ` +
          `stage=${marker.stage} name=${marker.name} index=${marker.index} total=${marker.total}`
      );
      callback(stage, marker);
    }
    if (resultText.includes("phase:start") && markers.length === 0) {
      console.warn(
        `[streamOutputHandler] tool_result contains "phase:start" but no marker parsed — ` +
          `possible format drift. Content: ${resultText.slice(0, 200)}`
      );
    }
  }
}

/**
 * Create a stream output handler that accumulates content_block_delta
 * fragments and flushes them as complete blocks.
 *
 * @param outputWindow - The OutputWindow instance to write entries to
 * @param options - Optional configuration including phase detection callback
 * @returns Object with onStdout, onStderr, and flush methods
 */
export function createStreamOutputHandler(
  outputWindow: OutputWindow,
  options?: StreamOutputHandlerOptions
) {
  const onPhaseDetected = options?.onPhaseDetected;

  // Per-stage delta buffers — each stage accumulates independently so
  // interleaved output from concurrent stages doesn't get mixed.
  const deltaBuffers = new Map<string, string>();

  function getBuffer(stage?: PipelineStage): string {
    return deltaBuffers.get(stage ?? "") ?? "";
  }

  function setBuffer(stage: PipelineStage | undefined, value: string): void {
    deltaBuffers.set(stage ?? "", value);
  }

  function flushDelta(stage?: PipelineStage): void {
    const key = stage ?? "";
    const buffer = deltaBuffers.get(key);
    if (buffer) {
      // Check for phase markers before sending to output window.
      // Phase markers are HTML comments that should not be rendered.
      if (!detectPhaseMarker(buffer, stage as PipelineStage, onPhaseDetected)) {
        outputWindow.appendLine(buffer, "info", stage);
      }
      deltaBuffers.set(key, "");
    }
  }

  /**
   * Emit text to the output window, suppressing phase markers.
   */
  function emitText(text: string, stage?: PipelineStage): void {
    if (!detectPhaseMarker(text, stage as PipelineStage, onPhaseDetected)) {
      outputWindow.appendLine(text, "info", stage);
    }
  }

  /**
   * Handle stdout data from a HeadlessOrchestrator callback.
   * Parses stream-json, accumulates content_block_delta fragments,
   * and flushes complete blocks to the OutputWindow.
   */
  function onStdout(stage: PipelineStage, data: string): void {
    for (const line of data.split("\n")) {
      if (!line.trim()) continue;

      // Filter stream-json envelopes (Issue #873)
      // But pass through content_block_stop as a flush signal.
      // Scan user messages for tool_result phase markers BEFORE filtering,
      // because printf-emitted markers arrive in tool_result envelopes that
      // would otherwise be silently discarded. (#3748)
      if (isStreamJsonEnvelope(line)) {
        if (line.trim().startsWith('{"type":"content_block_stop"')) {
          flushDelta(stage);
        }
        if (line.trim().startsWith('{"type":"user"')) {
          tryDetectToolResultPhaseMarkers(line, stage, onPhaseDetected);
        }
        continue;
      }

      try {
        const parsed = JSON.parse(line);

        if (parsed.type === "assistant" && parsed.message?.content) {
          // Complete assistant message — flush any pending delta first
          flushDelta(stage);
          for (const block of parsed.message.content) {
            if (block.type === "text" && block.text) {
              emitText(block.text, stage);
            } else if (block.type === "tool_use") {
              outputWindow.appendLine(`[Tool: ${block.name}]`, "tool", stage);
            }
          }
        } else if (parsed.type === "content_block_delta" && parsed.delta?.text) {
          const text = parsed.delta.text;
          // Skip assistant text that duplicates stage start/complete (Issue #770)
          if (/^(Starting |▶ Starting )/.test(text.trim())) {
            continue;
          }
          // Accumulate fragment
          setBuffer(stage, getBuffer(stage) + text);
        } else if (parsed.type === "content_block_stop") {
          flushDelta(stage);
        }
      } catch {
        // Not JSON — flush any pending delta, then use as plain text
        flushDelta(stage);
        if (line.trim() && !isEnvelopeFragment(line)) {
          emitText(line, stage);
        }
      }
    }
  }

  /**
   * Handle stderr data from a HeadlessOrchestrator callback.
   */
  function onStderr(stage: PipelineStage, data: string): void {
    for (const line of data.split("\n")) {
      if (!line.trim()) continue;
      // Filter stream-json envelopes and fragments (Issue #873)
      if (isStreamJsonEnvelope(line)) continue;
      if (isEnvelopeFragment(line)) continue;
      const isError = line.toLowerCase().includes("error") || line.toLowerCase().includes("failed");
      outputWindow.appendLine(line, isError ? "error" : "warning", stage);
    }
  }

  /**
   * Flush the delta buffer for a specific stage.
   *
   * Call this when a stage completes to ensure any remaining accumulated
   * content (including phase markers) is processed before the stage is
   * marked as done. Without this, the last phase marker can remain in
   * the buffer if no `content_block_stop` arrived before the subprocess
   * exited, leaving the final phase spinning indefinitely.
   */
  function flushStage(stage: PipelineStage): void {
    flushDelta(stage);
  }

  /**
   * Flush all remaining buffered content.  Call this when the pipeline
   * or stage completes to ensure no text is lost.
   */
  function flush(): void {
    for (const [key, buffer] of deltaBuffers.entries()) {
      if (buffer) {
        const stage = (key || undefined) as PipelineStage | undefined;
        if (!detectPhaseMarker(buffer, stage as PipelineStage, onPhaseDetected)) {
          outputWindow.appendLine(buffer, "info", stage);
        }
      }
    }
    deltaBuffers.clear();
  }

  return { onStdout, onStderr, flush, flushStage };
}
