/**
 * Agent HQ (Copilot SDK) Feasibility POC
 *
 * This file documents WHY GitHub Agent HQ is NOT viable for Nightgauge.
 * It is a research artifact, not production code.
 *
 * Issue: #509
 * Status: NOT VIABLE - Agent HQ is incompatible with our architecture
 */

/**
 * PROBLEM: Copilot SDK is designed for chat-style requests, not prompt injection.
 *
 * Nightgauge needs to:
 * 1. Inject SKILL.md content as system/user prompts
 * 2. Pass custom tool definitions (MCP format)
 * 3. Get structured stream-json output for token tracking
 * 4. Maintain context isolation per stage
 *
 * Copilot SDK provides:
 * 1. Pre-defined chat interactions with GitHub context
 * 2. GitHub-specific tool integrations (not MCP)
 * 3. Premium request billing model (not token-based)
 * 4. Opinionated workflows (PR review, issue triage, etc.)
 */

// ----------------------------------------------------------------------------
// CONCEPTUAL API (what we would need, but doesn't exist)
// ----------------------------------------------------------------------------

/**
 * What Nightgauge would need from Agent HQ:
 */
interface IdealAgentHQAPI {
  /**
   * Execute arbitrary prompt with custom tools
   * NOTE: This API does not exist in Copilot SDK
   */
  executePrompt(options: {
    systemPrompt: string; // Our SKILL.md content
    userPrompt: string; // Stage context
    tools: Tool[]; // MCP-style tool definitions
    outputFormat: "stream-json"; // For token tracking
  }): AsyncGenerator<Message>;
}

interface Tool {
  name: string;
  description: string;
  inputSchema: object;
}

interface Message {
  type: "text" | "tool_use" | "result";
  content: unknown;
  usage?: {
    input_tokens: number;
    output_tokens: number;
  };
}

// ----------------------------------------------------------------------------
// ACTUAL COPILOT SDK API (what actually exists)
// ----------------------------------------------------------------------------

/**
 * What Copilot SDK actually provides:
 * (Conceptual - based on research, not actual imports)
 */
interface ActualCopilotSDK {
  /**
   * Chat with Copilot in GitHub context
   * - Pre-defined system prompts
   * - GitHub-specific tools only
   * - No arbitrary prompt injection
   */
  chat(options: {
    messages: Array<{ role: "user" | "assistant"; content: string }>;
    context?: {
      repo?: string;
      pr?: number;
      issue?: number;
    };
  }): Promise<{
    message: string;
    // No token usage exposed
  }>;
}

// ----------------------------------------------------------------------------
// WHY THIS DOESN'T WORK
// ----------------------------------------------------------------------------

/**
 * Fundamental incompatibilities:
 *
 * 1. NO SYSTEM PROMPT INJECTION
 *    - Cannot pass SKILL.md content as system prompt
 *    - Copilot uses its own pre-defined prompts
 *
 * 2. NO CUSTOM TOOL DEFINITIONS
 *    - Cannot define MCP-style tools (Read, Write, Edit, Bash, etc.)
 *    - Copilot has GitHub-specific tools only (get_file, search_code, etc.)
 *
 * 3. NO TOKEN-LEVEL TRACKING
 *    - Premium request model doesn't expose token counts
 *    - Cannot integrate with our TokenTracker / TokenAccumulator
 *
 * 4. WRONG EXECUTION MODEL
 *    - Copilot is designed for conversational interactions
 *    - Nightgauge needs headless, single-shot prompt execution
 *    - No stream-json output format
 *
 * 5. GITHUB-CENTRIC
 *    - All context is GitHub-specific (repos, PRs, issues)
 *    - Our SKILL.md files operate on arbitrary codebases
 */

// ----------------------------------------------------------------------------
// CONCLUSION
// ----------------------------------------------------------------------------

/**
 * VERDICT: NOT VIABLE
 *
 * GitHub Agent HQ (Copilot SDK) is designed for a fundamentally different
 * use case than Nightgauge:
 *
 * - Copilot: GitHub-integrated code assistance with pre-defined workflows
 * - Nightgauge: Arbitrary AI agent pipeline execution with custom prompts
 *
 * If GitHub releases a more flexible API that supports:
 * - Custom system prompts
 * - MCP-style tool definitions
 * - Token-level usage tracking
 * - Stream output format
 *
 * Then this decision can be revisited. Until then, Agent HQ is not compatible
 * with Nightgauge's architecture.
 */

export {};
