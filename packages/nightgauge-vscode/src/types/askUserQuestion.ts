/**
 * AskUserQuestion type definitions
 *
 * Type definitions for the AskUserQuestion tool used during headless
 * pipeline execution. These types match Claude's AskUserQuestion tool schema.
 *
 * @see Issue #118 - AskUserQuestion tool support in headless mode
 */

/**
 * A single option for a question
 */
export interface QuestionOption {
  /** The display text for this option */
  label: string;
  /** Explanation of what this option means or what will happen if chosen */
  description?: string;
}

/**
 * A single question in the AskUserQuestion payload
 */
export interface Question {
  /** The complete question to ask the user */
  question: string;
  /** Very short label displayed as a chip/tag (max 12 chars) */
  header: string;
  /** The available choices for this question (2-4 options) */
  options: QuestionOption[];
  /** Whether multiple options can be selected */
  multiSelect: boolean;
}

/**
 * The full AskUserQuestion tool input payload
 * Matches the schema from Claude's AskUserQuestion tool
 */
export interface AskUserQuestionPayload {
  /** Array of questions to ask (1-4 questions) */
  questions: Question[];
  /** Optional metadata for tracking */
  metadata?: {
    source?: string;
  };
}

/**
 * Response for a single question
 */
export interface QuestionAnswer {
  /** The question index (q0, q1, etc.) */
  questionKey: string;
  /** The selected answer value(s) */
  answer: string | string[];
}

/**
 * The response format sent back to Claude CLI stdin
 * This matches the expected tool_result format
 */
export interface QuestionResponse {
  /** Map of question keys to answers */
  answers: Record<string, string | string[]>;
}

/**
 * Internal state for tracking an active question prompt
 */
export interface ActiveQuestionState {
  /** Unique identifier for this question session */
  id: string;
  /** The question payload being displayed */
  payload: AskUserQuestionPayload;
  /** Tool call ID from Claude CLI (for matching tool_result) */
  toolUseId?: string;
  /** Timestamp when the question was displayed */
  displayedAt: Date;
  /** Promise resolver to be called when user responds */
  resolve: (response: QuestionResponse | null) => void;
}

/**
 * Validate an AskUserQuestion payload from Claude CLI
 *
 * @param input - The parsed JSON input from tool_use block
 * @returns Validated payload or null if invalid
 */
export function validateAskUserQuestionPayload(input: unknown): AskUserQuestionPayload | null {
  if (!input || typeof input !== "object") {
    return null;
  }

  const payload = input as Record<string, unknown>;

  // Check questions array
  if (!Array.isArray(payload.questions)) {
    return null;
  }

  const questions: Question[] = [];
  for (const q of payload.questions) {
    if (!q || typeof q !== "object") {
      return null;
    }

    const question = q as Record<string, unknown>;

    // Validate required fields
    if (typeof question.question !== "string" || !question.question.trim()) {
      return null;
    }

    if (typeof question.header !== "string" || !question.header.trim()) {
      return null;
    }

    if (!Array.isArray(question.options) || question.options.length < 2) {
      return null;
    }

    // Validate options
    const options: QuestionOption[] = [];
    for (const opt of question.options) {
      if (!opt || typeof opt !== "object") {
        return null;
      }

      const option = opt as Record<string, unknown>;
      if (typeof option.label !== "string" || !option.label.trim()) {
        return null;
      }

      options.push({
        label: option.label,
        description: typeof option.description === "string" ? option.description : undefined,
      });
    }

    questions.push({
      question: question.question,
      header: question.header,
      options,
      multiSelect: question.multiSelect === true,
    });
  }

  // Validate question count (1-4)
  if (questions.length < 1 || questions.length > 4) {
    return null;
  }

  return {
    questions,
    metadata:
      payload.metadata && typeof payload.metadata === "object"
        ? (payload.metadata as { source?: string })
        : undefined,
  };
}

/**
 * Format a QuestionResponse for sending to Claude CLI stdin
 *
 * The response is formatted as a JSON object that matches what
 * Claude expects for AskUserQuestion tool results.
 *
 * @param response - The user's response
 * @returns JSON string to write to stdin
 */
export function formatResponseForStdin(response: QuestionResponse): string {
  // Format as the answers object that AskUserQuestion expects
  return JSON.stringify(response);
}

/**
 * Build a response from user selections
 *
 * @param selections - Map of question index to selected option(s)
 * @param payload - The original question payload
 * @returns Formatted response or null if invalid
 */
export function buildResponseFromSelections(
  selections: Map<number, string | string[]>,
  payload: AskUserQuestionPayload
): QuestionResponse | null {
  const answers: Record<string, string | string[]> = {};

  for (let i = 0; i < payload.questions.length; i++) {
    const selection = selections.get(i);
    if (selection === undefined) {
      // Missing answer for required question
      return null;
    }
    answers[`q${i}`] = selection;
  }

  return { answers };
}

/**
 * Generate a unique ID for a question session
 */
export function generateQuestionId(): string {
  return `question-${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
}
