/**
 * AWS Bedrock Backend POC
 *
 * This file demonstrates how Bedrock support would work in Nightgauge.
 * It shows the config-driven flag injection pattern - not a full implementation.
 *
 * Issue: #509
 * Status: VIABLE - Requires only config-level changes
 *
 * @see docs/MULTI_BACKEND_RESEARCH.md for full research findings
 */

import { spawn, type ChildProcess } from "child_process";

// ----------------------------------------------------------------------------
// CONFIG TYPES (from schema.ts)
// ----------------------------------------------------------------------------

/**
 * Authentication provider options
 * Already exists in packages/nightgauge-vscode/src/config/schema.ts
 */
type AuthProvider = "max" | "bedrock" | "vertex";

interface IncrediConfig {
  ui?: {
    core?: {
      auth_provider?: AuthProvider;
    };
  };
}

// ----------------------------------------------------------------------------
// IMPLEMENTATION PATTERN
// ----------------------------------------------------------------------------

/**
 * Build CLI arguments for Claude with backend-specific flags
 *
 * This is the key change needed in skillRunner.ts
 */
function buildClaudeArgs(config: IncrediConfig): string[] {
  const args = ["-p", "--output-format", "stream-json", "--verbose"];

  // Add backend flag based on config
  const authProvider = config.ui?.core?.auth_provider;

  switch (authProvider) {
    case "bedrock":
      args.push("--bedrock");
      break;
    case "vertex":
      args.push("--vertex");
      break;
    case "max":
    default:
      // 'max' is the default - no additional flag needed
      break;
  }

  return args;
}

/**
 * Example: Spawn Claude CLI with Bedrock backend
 */
function spawnClaudeWithBackend(config: IncrediConfig, prompt: string, cwd: string): ChildProcess {
  const args = buildClaudeArgs(config);

  const proc = spawn("claude", args, {
    cwd,
    shell: false,
    stdio: ["pipe", "pipe", "pipe"],
    env: {
      ...process.env,
      CI: "true", // Ensure non-interactive mode
    },
  });

  // Write prompt to stdin
  if (proc.stdin) {
    proc.stdin.write(prompt);
    proc.stdin.end();
  }

  return proc;
}

// ----------------------------------------------------------------------------
// EXAMPLE USAGE
// ----------------------------------------------------------------------------

/**
 * Example: Using Bedrock backend
 */
async function exampleBedrockUsage(): Promise<void> {
  const config: IncrediConfig = {
    ui: {
      core: {
        auth_provider: "bedrock",
      },
    },
  };

  const prompt = "Read the file package.json and summarize its contents.";
  const cwd = process.cwd();

  console.log("Spawning Claude with Bedrock backend...");
  console.log("Args:", buildClaudeArgs(config));

  // In real implementation, this would be in skillRunner.ts
  const proc = spawnClaudeWithBackend(config, prompt, cwd);

  proc.stdout?.on("data", (data) => {
    console.log("stdout:", data.toString());
  });

  proc.stderr?.on("data", (data) => {
    console.error("stderr:", data.toString());
  });

  proc.on("close", (code) => {
    console.log(`Process exited with code ${code}`);
  });
}

// ----------------------------------------------------------------------------
// AWS PREREQUISITES
// ----------------------------------------------------------------------------

/**
 * For Bedrock to work, the following must be configured:
 *
 * 1. AWS CREDENTIALS
 *    - AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables
 *    - Or ~/.aws/credentials file
 *    - Or IAM role (on EC2/ECS/Lambda)
 *
 * 2. AWS REGION
 *    - AWS_REGION or AWS_DEFAULT_REGION environment variable
 *    - Must be a region where Bedrock is available
 *
 * 3. MODEL ACCESS
 *    - Claude models must be enabled in AWS Bedrock console
 *    - See: AWS Console > Bedrock > Model access
 *
 * 4. IAM PERMISSIONS
 *    - bedrock:InvokeModel
 *    - bedrock:InvokeModelWithResponseStream
 *    - See docs/MULTI_BACKEND_RESEARCH.md for full IAM policy
 */

// ----------------------------------------------------------------------------
// IMPLEMENTATION LOCATIONS
// ----------------------------------------------------------------------------

/**
 * Files that need modification for full Bedrock support:
 *
 * 1. packages/nightgauge-vscode/src/utils/skillRunner.ts
 *    - Import config
 *    - Add auth_provider flag to CLI args (as shown above)
 *
 * 2. packages/nightgauge-vscode/src/services/HeadlessOrchestrator.ts
 *    - Pass config to skill runner
 *
 * 3. docs/CONFIGURATION.md
 *    - Document auth_provider options
 *    - Document AWS prerequisites
 *
 * The token parsing (tokenParser.ts) does NOT need changes because
 * Bedrock uses the same stream-json output format as Claude Max.
 */

export { buildClaudeArgs, spawnClaudeWithBackend };
