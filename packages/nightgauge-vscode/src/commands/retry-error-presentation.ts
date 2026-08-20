import { isRecoveryRequiredError } from "../orchestrator/recovery/computeRecoveryRequired";

/**
 * Failures already presented by the structured Recovery Dialog.
 *
 * Flat retry toasts must suppress these errors because their messages can
 * contain local paths and would duplicate the recovery UI.
 */
export function isRecoveryShapedError(error: unknown): boolean {
  return isRecoveryRequiredError(error);
}

export function getRetryStatusMessage(error: unknown, fallback: string): string {
  if (isRecoveryShapedError(error)) {
    return "Recovery required";
  }
  return error instanceof Error ? error.message : String(error ?? fallback);
}
