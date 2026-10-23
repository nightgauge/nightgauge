import { describe, it, expect } from "vitest";
import {
  validateAskUserQuestionPayload,
  formatResponseForStdin,
  buildResponseFromSelections,
  generateQuestionId,
  type AskUserQuestionPayload,
} from "../../src/types/askUserQuestion";

describe("askUserQuestion", () => {
  describe("validateAskUserQuestionPayload", () => {
    it("should validate a valid single question payload", () => {
      const input = {
        questions: [
          {
            question: "Which option do you prefer?",
            header: "Preference",
            options: [
              { label: "Option A", description: "First option" },
              { label: "Option B", description: "Second option" },
            ],
            multiSelect: false,
          },
        ],
      };

      const result = validateAskUserQuestionPayload(input);

      expect(result).not.toBeNull();
      expect(result?.questions).toHaveLength(1);
      expect(result?.questions[0].question).toBe("Which option do you prefer?");
      expect(result?.questions[0].header).toBe("Preference");
      expect(result?.questions[0].options).toHaveLength(2);
      expect(result?.questions[0].multiSelect).toBe(false);
    });

    it("should validate a payload with multiple questions", () => {
      const input = {
        questions: [
          {
            question: "Question 1?",
            header: "Q1",
            options: [{ label: "A" }, { label: "B" }],
            multiSelect: false,
          },
          {
            question: "Question 2?",
            header: "Q2",
            options: [{ label: "X" }, { label: "Y" }, { label: "Z" }],
            multiSelect: true,
          },
        ],
      };

      const result = validateAskUserQuestionPayload(input);

      expect(result).not.toBeNull();
      expect(result?.questions).toHaveLength(2);
      expect(result?.questions[1].multiSelect).toBe(true);
    });

    it("should include optional metadata", () => {
      const input = {
        questions: [
          {
            question: "Test?",
            header: "Test",
            options: [{ label: "A" }, { label: "B" }],
            multiSelect: false,
          },
        ],
        metadata: { source: "test-source" },
      };

      const result = validateAskUserQuestionPayload(input);

      expect(result?.metadata?.source).toBe("test-source");
    });

    it("should return null for invalid input (null)", () => {
      expect(validateAskUserQuestionPayload(null)).toBeNull();
    });

    it("should return null for invalid input (undefined)", () => {
      expect(validateAskUserQuestionPayload(undefined)).toBeNull();
    });

    it("should return null for invalid input (no questions array)", () => {
      expect(validateAskUserQuestionPayload({ foo: "bar" })).toBeNull();
    });

    it("should return null for invalid input (empty questions array)", () => {
      expect(validateAskUserQuestionPayload({ questions: [] })).toBeNull();
    });

    it("should return null for invalid input (too many questions)", () => {
      const input = {
        questions: Array(5).fill({
          question: "Q?",
          header: "H",
          options: [{ label: "A" }, { label: "B" }],
          multiSelect: false,
        }),
      };
      expect(validateAskUserQuestionPayload(input)).toBeNull();
    });

    it("should return null for invalid question (missing question text)", () => {
      const input = {
        questions: [
          {
            header: "Test",
            options: [{ label: "A" }, { label: "B" }],
            multiSelect: false,
          },
        ],
      };
      expect(validateAskUserQuestionPayload(input)).toBeNull();
    });

    it("should return null for invalid question (missing header)", () => {
      const input = {
        questions: [
          {
            question: "Test?",
            options: [{ label: "A" }, { label: "B" }],
            multiSelect: false,
          },
        ],
      };
      expect(validateAskUserQuestionPayload(input)).toBeNull();
    });

    it("should return null for invalid question (less than 2 options)", () => {
      const input = {
        questions: [
          {
            question: "Test?",
            header: "Test",
            options: [{ label: "A" }],
            multiSelect: false,
          },
        ],
      };
      expect(validateAskUserQuestionPayload(input)).toBeNull();
    });

    it("should return null for invalid option (missing label)", () => {
      const input = {
        questions: [
          {
            question: "Test?",
            header: "Test",
            options: [{ description: "No label" }, { label: "B" }],
            multiSelect: false,
          },
        ],
      };
      expect(validateAskUserQuestionPayload(input)).toBeNull();
    });
  });

  describe("formatResponseForStdin", () => {
    it("should format a simple response", () => {
      const response = {
        answers: { q0: "Option A" },
      };

      const result = formatResponseForStdin(response);

      expect(result).toBe('{"answers":{"q0":"Option A"}}');
    });

    it("should format a multi-select response", () => {
      const response = {
        answers: { q0: ["Option A", "Option B"] },
      };

      const result = formatResponseForStdin(response);

      expect(result).toBe('{"answers":{"q0":["Option A","Option B"]}}');
    });

    it("should format multiple question responses", () => {
      const response = {
        answers: {
          q0: "Answer 1",
          q1: ["A", "B"],
        },
      };

      const result = formatResponseForStdin(response);

      expect(JSON.parse(result)).toEqual(response);
    });
  });

  describe("buildResponseFromSelections", () => {
    const samplePayload: AskUserQuestionPayload = {
      questions: [
        {
          question: "Q1?",
          header: "Q1",
          options: [{ label: "A" }, { label: "B" }],
          multiSelect: false,
        },
        {
          question: "Q2?",
          header: "Q2",
          options: [{ label: "X" }, { label: "Y" }],
          multiSelect: true,
        },
      ],
    };

    it("should build response from complete selections", () => {
      const selections = new Map<number, string | string[]>([
        [0, "A"],
        [1, ["X", "Y"]],
      ]);

      const result = buildResponseFromSelections(selections, samplePayload);

      expect(result).not.toBeNull();
      expect(result?.answers.q0).toBe("A");
      expect(result?.answers.q1).toEqual(["X", "Y"]);
    });

    it("should return null for incomplete selections", () => {
      const selections = new Map<number, string | string[]>([[0, "A"]]);

      const result = buildResponseFromSelections(selections, samplePayload);

      expect(result).toBeNull();
    });

    it("should return null for empty selections", () => {
      const selections = new Map<number, string | string[]>();

      const result = buildResponseFromSelections(selections, samplePayload);

      expect(result).toBeNull();
    });
  });

  describe("generateQuestionId", () => {
    it("should generate unique IDs", () => {
      const id1 = generateQuestionId();
      const id2 = generateQuestionId();

      expect(id1).not.toBe(id2);
    });

    it("should generate IDs with expected format", () => {
      const id = generateQuestionId();

      expect(id).toMatch(/^question-\d+-[a-z0-9]+$/);
    });
  });
});
