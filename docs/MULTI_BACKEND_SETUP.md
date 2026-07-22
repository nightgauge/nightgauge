# Multi-Backend Setup Guide

This guide provides complete setup instructions for using AWS Bedrock and Google
Cloud Vertex as alternative backends for Nightgauge.

## Overview

Nightgauge supports multiple AI backends. The table distinguishes agentic
pipeline backends from chat-completion-only evaluation backends:

| Backend     | Flag         | Use Case                            | Pricing           |
| ----------- | ------------ | ----------------------------------- | ----------------- |
| Claude Max  | (default)    | Direct Anthropic API access         | Anthropic rates   |
| AWS Bedrock | `--bedrock`  | Enterprise AWS integration          | AWS Bedrock rates |
| GCP Vertex  | `--vertex`   | Enterprise Google Cloud integration | GCP Vertex rates  |
| Gemini CLI  | `gemini`     | Experimental agentic pipeline       | Google AI rates   |
| Gemini SDK  | `gemini-sdk` | Chat-only evaluation via API key    | Google AI rates   |
| LM Studio   | `lm-studio`  | Chat-only local evaluation          | Free (local)      |

**When to use alternative backends:**

- **Consolidated billing** - Route AI costs through existing AWS/GCP accounts
- **Compliance requirements** - Data residency, VPC isolation, audit logging
- **Enterprise controls** - IAM-based access, private endpoints, CloudTrail

## Backend Comparison

| Factor              | Claude Max       | AWS Bedrock             | GCP Vertex             |
| ------------------- | ---------------- | ----------------------- | ---------------------- |
| Billing             | Direct Anthropic | AWS consolidated        | GCP consolidated       |
| VPC/Private         | No               | Yes (PrivateLink)       | Yes (VPC-SC)           |
| Audit Logging       | Limited          | CloudTrail              | Cloud Audit Logs       |
| Rate Limits         | Account-based    | AWS service limits      | GCP service limits     |
| IAM Integration     | API keys         | IAM policies            | IAM + Service Accounts |
| Region Availability | Global           | US-East-1, US-West-2, + | US-Central1, Europe, + |

---

## AWS Bedrock Setup

### Prerequisites

1. AWS account with Bedrock access enabled
2. Claude models enabled in your target region
3. IAM permissions for `bedrock:InvokeModel`

### Step 1: Enable Model Access

Bedrock requires explicit model access enablement per region:

1. Open AWS Console > Amazon Bedrock > Model access
2. Select your target region (e.g., `us-east-1`)
3. Click "Manage model access"
4. Find "Anthropic" > "Claude" models
5. Request access (approval is usually instant)

**Supported regions for Claude on Bedrock:**

- `us-east-1` (N. Virginia)
- `us-west-2` (Oregon)
- `eu-west-1` (Ireland)
- `ap-northeast-1` (Tokyo)

Check [AWS Bedrock documentation](https://docs.aws.amazon.com/bedrock/) for
current region availability.

### Step 2: Create IAM Policy

Create a policy with minimum required permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "BedrockClaudeAccess",
      "Effect": "Allow",
      "Action": ["bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"],
      "Resource": "arn:aws:bedrock:*:*:foundation-model/anthropic.claude-*"
    }
  ]
}
```

**Optional: Restrict to specific models and regions:**

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "BedrockClaudeRestricted",
      "Effect": "Allow",
      "Action": ["bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"],
      "Resource": [
        "arn:aws:bedrock:us-east-1:*:foundation-model/anthropic.claude-sonnet-4-6-*",
        "arn:aws:bedrock:us-east-1:*:foundation-model/anthropic.claude-opus-4-7-*"
      ]
    }
  ]
}
```

### Step 3: Configure Credentials

Choose one of these credential methods:

#### Option A: Environment Variables

```bash
export AWS_ACCESS_KEY_ID=AKIA...
export AWS_SECRET_ACCESS_KEY=...
export AWS_REGION=us-east-1
```

#### Option B: AWS Config File

```ini
# ~/.aws/credentials
[default]
aws_access_key_id = AKIA...
aws_secret_access_key = ...

# ~/.aws/config
[default]
region = us-east-1
```

#### Option C: IAM Roles (Recommended for EC2/ECS/Lambda)

Attach the Bedrock IAM policy to your compute resource's role. No explicit
credentials needed.

### Step 4: Configure Nightgauge

```yaml
# .nightgauge/config.yaml
ui:
  core:
    auth_provider: bedrock
```

**Environment variable override:**

```bash
export NIGHTGAUGE_UI_CORE_AUTH_PROVIDER=bedrock
```

### Step 5: Verify Configuration

```bash
# Test AWS credentials
aws sts get-caller-identity

# Test Bedrock access (requires AWS CLI v2)
aws bedrock list-foundation-models --region us-east-1 --query 'modelSummaries[?contains(modelId, `claude`)]'
```

---

## Google Cloud Vertex Setup

### Prerequisites

1. GCP project with Vertex AI API enabled
2. Service account with Vertex AI permissions
3. Claude models enabled via Model Garden

### Step 1: Enable Vertex AI API

```bash
# Enable the API
gcloud services enable aiplatform.googleapis.com

# Verify it's enabled
gcloud services list --enabled | grep aiplatform
```

### Step 2: Enable Claude Models

1. Open GCP Console > Vertex AI > Model Garden
2. Search for "Claude"
3. Enable the Claude models you need
4. Note the model endpoint region

**Supported regions for Claude on Vertex:**

- `us-central1` (Iowa)
- `europe-west1` (Belgium)
- `asia-northeast1` (Tokyo)

Check
[Vertex AI documentation](https://cloud.google.com/vertex-ai/docs/generative-ai/model-garden/explore-models)
for current availability.

### Step 3: Create Service Account

```bash
# Create service account
gcloud iam service-accounts create nightgauge-vertex \
    --display-name="Nightgauge Vertex Access"

# Grant Vertex AI User role
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
    --member="serviceAccount:nightgauge-vertex@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/aiplatform.user"

# Create and download key (for local development)
gcloud iam service-accounts keys create ~/nightgauge-vertex-key.json \
    --iam-account=nightgauge-vertex@YOUR_PROJECT_ID.iam.gserviceaccount.com
```

**Required IAM role:** `roles/aiplatform.user`

This role includes:

- `aiplatform.endpoints.predict`
- `aiplatform.models.predict`

### Step 4: Configure Credentials

#### Option A: Application Default Credentials (Local Development)

```bash
# Login with your user account
gcloud auth application-default login

# Or use service account key
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/nightgauge-vertex-key.json
```

#### Option B: Workload Identity (GKE/Cloud Run)

Configure workload identity federation to use the service account without key
files.

### Step 5: Configure Nightgauge

```yaml
# .nightgauge/config.yaml
ui:
  core:
    auth_provider: vertex
```

**Environment variable override:**

```bash
export NIGHTGAUGE_UI_CORE_AUTH_PROVIDER=vertex
```

### Step 6: Verify Configuration

```bash
# Test GCP authentication
gcloud auth application-default print-access-token

# Verify project
gcloud config get-value project
```

---

## Gemini Setup

Nightgauge supports Google's Gemini models via two adapters: the Gemini CLI
adapter and the Gemini SDK adapter.

### Gemini CLI Adapter (`gemini`)

Uses the `gemini` CLI tool (v0.29.0+ required for stream-json support).

#### Step 1: Install Gemini CLI

```bash
# Verify installation
gemini --version  # Must be 0.29.0+
```

#### Step 2: Authenticate

```bash
# Login with Google account
gemini auth login
```

#### Step 3: Configure Nightgauge

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: gemini
```

**Optional environment variables:**

```bash
export NIGHTGAUGE_GEMINI_CLI_COMMAND=gemini       # Override CLI binary
export NIGHTGAUGE_GEMINI_CLI_ARGS=""               # Extra CLI arguments
export NIGHTGAUGE_GEMINI_MODEL=gemini-2.5-flash   # Override model
```

### Gemini SDK Adapter (`gemini-sdk`)

Uses the `@google/genai` SDK with an API key — no CLI installation required.

> **Scope:** Chat-completion-only. Use this adapter for evaluation, judging, or
> summarization. Pipeline stages require the agentic Gemini CLI adapter.

#### Step 1: Get API Key

1. Go to [Google AI Studio](https://aistudio.google.com/)
2. Create or select a project
3. Generate an API key

#### Step 2: Configure Credentials

```bash
# Primary API key
export GEMINI_API_KEY=your-api-key

# Or use the Google-wide key (fallback)
export GOOGLE_API_KEY=your-api-key

# Optional: Use Vertex AI endpoint instead of public API
export GOOGLE_GENAI_USE_VERTEXAI=true
```

#### Step 3: Configure Nightgauge

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: gemini-sdk
```

**Or via VSCode settings:**

| Setting                        | Value              |
| ------------------------------ | ------------------ |
| `nightgauge.gemini.authMethod` | `api-key`          |
| `nightgauge.gemini.model`      | `gemini-2.5-flash` |
| `nightgauge.gemini.apiKey`     | _(your API key)_   |

#### Step 4: Verify

```bash
# Test that the adapter can authenticate
# The SDK adapter checks for GEMINI_API_KEY or GOOGLE_API_KEY presence
echo $GEMINI_API_KEY  # Should be set
```

### Gemini Context File

When using either Gemini adapter, Nightgauge automatically generates a
`GEMINI.md` context file before each stage execution. This provides Gemini with
project context (analogous to `CLAUDE.md`). See
[CONFIGURATION.md](./CONFIGURATION.md) for `pipeline.gemini_context` settings.

---

## LM Studio (Local Model)

LM Studio runs models locally on your machine using an OpenAI-compatible REST
API. No API key or internet connection is required after models are downloaded.

LM Studio is chat-completion-only: Nightgauge supports it for evaluation,
judging, and summarization, not pipeline execution. It cannot edit files, run
shell commands, or call `gh`.

### When to use LM Studio

**Recommended when:**

- You want zero-cost local inference for development and experimentation
- Data cannot leave your machine (air-gapped, compliance, privacy requirements)
- You are running offline or on unreliable internet
- You want to evaluate pipeline behavior with open-weight models

**Not recommended when:**

- You need reliable tool calling (model-dependent, disabled by default)
- You require performance parity with hosted Claude/Gemini models
- You need to run any pipeline stage — LM Studio does not provide Nightgauge's
  required agentic tool loop
- Token context limits matter — local models often have smaller context windows
  than hosted providers

### Prerequisites

1. LM Studio v0.3.x or later installed (<https://lmstudio.ai/>)
2. At least one chat model downloaded in LM Studio
3. LM Studio local server enabled (Settings → Local Server, port 1234)
4. A model that supports text generation (tool calling requires additional model
   capability — see [Local Model Limitations](#local-model-limitations) below)

### Step 1: Enable LM Studio Local Server

1. Open LM Studio
2. Go to Settings → Local Server (or the server icon in the left sidebar)
3. Enable the server — it starts on `http://localhost:1234` by default
4. Load the model you want to use in the chat window (or via the server
   controls)
5. Verify the server is running:

```bash
curl http://localhost:1234/v1/models
```

### Step 2: Configure Nightgauge

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: lm-studio
    default_model: sonnet # Ignored for LM Studio — model set via lm_studio.model

lm_studio:
  model: "lmstudio-community/Meta-Llama-3.1-8B-Instruct-GGUF" # Your loaded model name
  base_url: "http://localhost:1234/v1" # Default; change if using non-standard port
  api_key: "lm-studio" # LM Studio ignores this value; required by protocol
  timeout_ms: 180000 # 3 minutes — local inference can be slow
  max_tokens: 8192
  stream_options:
    include_usage: true # Required for token count reporting
  tool_calling: false # Disabled by default; model-dependent
```

### Step 3: Verify Setup

```bash
# Verify LM Studio server responds and list available models
curl -s http://localhost:1234/v1/models | jq '.data[].id'
```

### LM Studio Adapter Environment Variables

| Variable                          | Description                          | Default                    |
| --------------------------------- | ------------------------------------ | -------------------------- |
| `NIGHTGAUGE_LM_STUDIO_BASE_URL`   | LM Studio server base URL            | `http://localhost:1234/v1` |
| `NIGHTGAUGE_LM_STUDIO_MODEL`      | Model name to use                    | _(required)_               |
| `NIGHTGAUGE_LM_STUDIO_API_KEY`    | API key (value ignored by LM Studio) | `lm-studio`                |
| `NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS` | Request timeout in milliseconds      | `180000`                   |

### Local Model Limitations

LM Studio support is intentionally scoped to avoid misrepresenting local
inference capabilities:

**Performance:**

- Inference speed depends on your hardware — expect 10–100× slower generation
  than hosted providers on typical developer machines
- Large pipeline stages (feature-dev, feature-planning) involve long prompts and
  can take 10–30 minutes per stage on CPU-only machines

**Model-level tool calling:**

- Disabled by default (`tool_calling: false`)
- Tool calling is model-dependent — not all models that claim OpenAI
  compatibility implement function calling reliably
- When enabled (`tool_calling: true`), results vary widely by model. This does
  not turn LM Studio into a Nightgauge pipeline adapter.

**Token accounting:**

- Token counts are reported from LM Studio when
  `stream_options.include_usage: true` is set (required in streaming mode)
- Token usage in non-streaming mode is always available in the response body
- Cost display shows `$0.0000` for all local inference — this is correct; local
  models have no API cost

**Context window:**

- Local models vary in context window size — commonly 4K–32K tokens
- The pipeline stages were designed for models with 100K+ context windows;
  stages may truncate or fail with smaller context windows
- Check your model's context limit in LM Studio before running full pipelines

**Phase marker detection:**

- LM Studio can pass through HTML comments (phase markers) in model output;
  validate the behavior against the model and LM Studio version you deploy
- Actual passthrough fidelity depends on the loaded model and its
  instruction-following capability; results vary across models
- Streaming phase markers require accumulation across SSE chunks — live
  validation required before relying on this in production

**Quality variability:**

- Open-weight local models produce significantly more variable output than
  frontier models (Claude, Gemini)
- Pipeline stages that depend on structured JSON output (context files, plan
  files) may fail more often with local models
- Expect higher retry rates and more manual intervention

### Troubleshooting

| Error                               | Likely Cause                         | Solution                                                 |
| ----------------------------------- | ------------------------------------ | -------------------------------------------------------- |
| `Connection refused localhost:1234` | LM Studio server not running         | Enable local server in LM Studio settings                |
| `Model not loaded`                  | No model selected in LM Studio       | Load a model in LM Studio chat or server controls        |
| `Request timeout`                   | Inference too slow for timeout       | Increase `lm_studio.timeout_ms` or use a smaller model   |
| `Empty response / no content`       | Model context window exceeded        | Use a smaller prompt or switch to a larger-context model |
| `Tool call failed`                  | Model does not support tool calling  | Set `lm_studio.tool_calling: false`                      |
| `$0.0000 cost shown`                | Expected — local models have no cost | This is correct behavior                                 |

---

## Pricing Comparison

Pricing as of February 2026 (check provider documentation for current rates):

| Backend     | Input Cost   | Output Cost   | Est. Cost/Stage |
| ----------- | ------------ | ------------- | --------------- |
| Claude Max  | ~$3/M tokens | ~$15/M tokens | ~$0.03-0.10     |
| AWS Bedrock | ~$3/M tokens | ~$15/M tokens | ~$0.03-0.10     |
| GCP Vertex  | ~$3/M tokens | ~$15/M tokens | ~$0.03-0.10     |

**Notes:**

- Pricing is similar across backends for the same models
- Enterprise discounts may apply based on committed usage
- Bedrock charges may include additional AWS data transfer costs
- Vertex charges may include additional GCP networking costs
- Check your enterprise agreements for volume discounts

**Cost optimization:**

- Nightgauge uses context isolation (~5K tokens/stage), keeping costs low
- Caching can reduce input token costs significantly
- Consider reserved capacity for high-volume usage

---

## Enterprise Considerations

### AWS Bedrock Benefits

| Capability           | Description                                 |
| -------------------- | ------------------------------------------- |
| Consolidated Billing | Route AI costs through existing AWS account |
| VPC PrivateLink      | Private endpoints, no public internet       |
| CloudTrail           | Full API audit logging                      |
| IAM Policies         | Fine-grained access control                 |
| Service Quotas       | Adjustable rate limits                      |
| Cost Explorer        | Native AWS cost analysis                    |

**VPC PrivateLink setup:**

```bash
# Create VPC endpoint for Bedrock
aws ec2 create-vpc-endpoint \
    --vpc-id vpc-xxx \
    --service-name com.amazonaws.us-east-1.bedrock-runtime \
    --vpc-endpoint-type Interface
```

### Google Cloud Vertex Benefits

| Capability           | Description                                 |
| -------------------- | ------------------------------------------- |
| Consolidated Billing | Route AI costs through existing GCP account |
| VPC Service Controls | Data exfiltration prevention                |
| Cloud Audit Logs     | Full API audit logging                      |
| IAM + Workload ID    | Service account-based access                |
| Quotas               | Adjustable rate limits                      |
| Cost Management      | Native GCP billing analysis                 |

**VPC Service Controls setup:**

```bash
# Create access level and perimeter (simplified)
gcloud access-context-manager perimeters create nightgauge-perimeter \
    --resources=projects/YOUR_PROJECT_NUMBER \
    --restricted-services=aiplatform.googleapis.com
```

---

## Configuration Examples

### Basic Bedrock Configuration

```yaml
# .nightgauge/config.yaml
ui:
  core:
    auth_provider: bedrock
    default_model: sonnet

# AWS credentials via environment:
# export AWS_REGION=us-east-1
# export AWS_ACCESS_KEY_ID=...
# export AWS_SECRET_ACCESS_KEY=...
```

### Basic Vertex Configuration

```yaml
# .nightgauge/config.yaml
ui:
  core:
    auth_provider: vertex
    default_model: sonnet

# GCP credentials via environment:
# export GOOGLE_APPLICATION_CREDENTIALS=/path/to/key.json
# Or: gcloud auth application-default login
```

### CI/CD Configuration

```yaml
# GitHub Actions example
jobs:
  pipeline:
    runs-on: self-hosted
    permissions:
      id-token: write # For OIDC
      contents: read
    steps:
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::123456789:role/nightgauge-ci
          aws-region: us-east-1

      - name: Run Nightgauge Pipeline
        env:
          NIGHTGAUGE_UI_CORE_AUTH_PROVIDER: bedrock
        run: |
          npm run nightgauge:pipeline
```

---

## Troubleshooting

For common multi-backend issues, see
[TROUBLESHOOTING.md#multi-backend-issues](./TROUBLESHOOTING.md#multi-backend-issues).

**Quick reference:**

| Error                              | Likely Cause             | Solution                     |
| ---------------------------------- | ------------------------ | ---------------------------- |
| AccessDeniedException              | Missing IAM permissions  | Check IAM policy             |
| Model not available                | Model access not enabled | Enable in Bedrock/Vertex     |
| Region not supported               | Wrong region configured  | Check region availability    |
| Could not load default credentials | Missing GCP credentials  | Run `gcloud auth` or set env |
| UnrecognizedClientException        | Invalid AWS credentials  | Check AWS_ACCESS_KEY_ID      |

---

## Related Documentation

- [CONFIGURATION.md](./CONFIGURATION.md) - Full configuration reference
- [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) - General troubleshooting
- [ADAPTER_MATRIX.md](./ADAPTER_MATRIX.md) - Supported adapter capabilities
- [ARCHITECTURE.md](./ARCHITECTURE.md) - System architecture

---

## Author

nightgauge
