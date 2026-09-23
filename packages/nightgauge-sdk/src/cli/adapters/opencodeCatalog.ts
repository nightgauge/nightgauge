/**
 * OpenCode's provider catalog, as the environment variables it binds to each
 * provider key (ADR-022 § 8).
 *
 * OpenCode loads a catalog provider when any one of its variables is set and
 * reads that provider's credentials from them, so a variable here that reaches
 * a run makes its provider reachable. The OpenCode child-env curation
 * (childEnv.ts) forwards only the dispatched provider's entry, and the
 * adapter's credential check (OpenCodeAdapter.ts) requires one of them for a
 * hosted provider.
 *
 * This is a copy of `openCodeCatalogEnv` in
 * internal/execution/adapters/opencode_catalog_env.go, which is read from the
 * opencode 1.18.30 binary. The drift guard in tests/cli/childEnv.test.ts parses
 * that Go file and fails when the two differ, so change the Go map first and
 * copy it here.
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md § 8
 * @see Issue #1637
 */

export const OPENCODE_CATALOG_ENV: Readonly<Record<string, readonly string[]>> = Object.freeze({
  "302ai": ["302AI_API_KEY"],
  abacus: ["ABACUS_API_KEY"],
  "abliteration-ai": ["ABLIT_KEY"],
  above: ["ABOVE_API_KEY"],
  agentrouter: ["AGENTROUTER_API_KEY"],
  agnes: ["AGNES_API_KEY"],
  "ai-router": ["AI_ROUTER_API_KEY"],
  aiand: ["AIAND_API_KEY"],
  aihubmix: ["AIHUBMIX_API_KEY"],
  aixy: ["AIXY_API_KEY"],
  "aki-io": ["AKI_IO_API_KEY"],
  alibaba: ["DASHSCOPE_API_KEY"],
  "alibaba-cn": ["DASHSCOPE_API_KEY"],
  "alibaba-coding-plan": ["ALIBABA_CODING_PLAN_API_KEY"],
  "alibaba-coding-plan-cn": ["ALIBABA_CODING_PLAN_API_KEY"],
  "alibaba-token-plan": ["ALIBABA_TOKEN_PLAN_API_KEY"],
  "alibaba-token-plan-cn": ["ALIBABA_TOKEN_PLAN_API_KEY"],
  "amazon-bedrock": [
    "AWS_ACCESS_KEY_ID",
    "AWS_SECRET_ACCESS_KEY",
    "AWS_REGION",
    "AWS_BEARER_TOKEN_BEDROCK",
  ],
  ambient: ["AMBIENT_API_KEY"],
  amd: ["AMD_API_KEY"],
  anthropic: ["ANTHROPIC_API_KEY"],
  anyapi: ["ANYAPI_API_KEY"],
  arcee: ["ARCEE_API_KEY"],
  "atomic-chat": ["ATOMIC_CHAT_API_KEY"],
  auriko: ["AURIKO_API_KEY"],
  azure: ["AZURE_RESOURCE_NAME", "AZURE_API_KEY"],
  "azure-cognitive-services": [
    "AZURE_COGNITIVE_SERVICES_RESOURCE_NAME",
    "AZURE_COGNITIVE_SERVICES_API_KEY",
  ],
  bailing: ["BAILING_API_TOKEN"],
  baseten: ["BASETEN_API_KEY"],
  berget: ["BERGET_API_KEY"],
  blueclaw: ["BLUECLAW_API_KEY"],
  bothub: ["BOTHUB_API_KEY"],
  cerebras: ["CEREBRAS_API_KEY"],
  chutes: ["CHUTES_API_KEY"],
  clarifai: ["CLARIFAI_PAT"],
  claudinio: ["CLAUDINIO_API_KEY"],
  "cline-pass": ["CLINE_API_KEY"],
  "cloudferro-sherlock": ["CLOUDFERRO_SHERLOCK_API_KEY"],
  "cloudflare-ai-gateway": [
    "CLOUDFLARE_API_TOKEN",
    "CLOUDFLARE_ACCOUNT_ID",
    "CLOUDFLARE_GATEWAY_ID",
  ],
  "cloudflare-workers-ai": ["CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_API_KEY"],
  cohere: ["COHERE_API_KEY"],
  coralbricks: ["CORAL_API_KEY"],
  cortecs: ["CORTECS_API_KEY"],
  crof: ["CROF_API_KEY"],
  crossmodel: ["CROSSMODEL_API_KEY"],
  crusoe: ["CRUSOE_API_KEY"],
  daoxe: ["DAOXE_API_KEY"],
  databricks: ["DATABRICKS_HOST", "DATABRICKS_TOKEN"],
  deepinfra: ["DEEPINFRA_API_KEY"],
  deepseek: ["DEEPSEEK_API_KEY"],
  digitalocean: ["DIGITALOCEAN_ACCESS_TOKEN"],
  dinference: ["DINFERENCE_API_KEY"],
  drun: ["DRUN_API_KEY"],
  ebcloud: ["EBCLOUD_API_KEY"],
  echo: ["ECHO_API_KEY"],
  edenai: ["EDENAI_API_KEY"],
  empiriolabs: ["EMPIRIOLABS_API_KEY"],
  evroc: ["EVROC_API_KEY"],
  fastrouter: ["FASTROUTER_API_KEY"],
  "fireworks-ai": ["FIREWORKS_API_KEY"],
  freemodel: ["FREEMODEL_API_KEY"],
  friendli: ["FRIENDLI_TOKEN"],
  frogbot: ["FROGBOT_API_KEY"],
  "github-copilot": ["GITHUB_TOKEN"],
  gitlab: ["GITLAB_TOKEN"],
  gmicloud: ["GMICLOUD_API_KEY"],
  google: ["GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"],
  "google-vertex": [
    "GOOGLE_VERTEX_PROJECT",
    "GOOGLE_VERTEX_LOCATION",
    "GOOGLE_APPLICATION_CREDENTIALS",
  ],
  "google-vertex-anthropic": [
    "GOOGLE_VERTEX_PROJECT",
    "GOOGLE_VERTEX_LOCATION",
    "GOOGLE_APPLICATION_CREDENTIALS",
  ],
  greenpt: ["GREENPT_API_KEY"],
  groq: ["GROQ_API_KEY"],
  helicone: ["HELICONE_API_KEY"],
  hetzner: ["HETZNER_API_KEY"],
  "hpc-ai": ["HPC_AI_API_KEY"],
  huggingface: ["HF_TOKEN"],
  hyper: ["HYPER_API_KEY"],
  iflowcn: ["IFLOW_API_KEY"],
  impossibl: ["IMPOSSIBL_API_KEY"],
  inception: ["INCEPTION_API_KEY"],
  inceptron: ["INCEPTRON_API_KEY"],
  inference: ["INFERENCE_API_KEY"],
  inferx: ["INFERX_API_KEY"],
  infomaniak: ["INFOMANIAK_API_KEY", "INFOMANIAK_PRODUCT_ID"],
  "io-net": ["IOINTELLIGENCE_API_KEY"],
  iteracompute: ["ITERACOMPUTE_API_KEY"],
  jalapeno: ["JALAPENO_API_KEY"],
  jiekou: ["JIEKOU_API_KEY"],
  kenari: ["KENARI_API_KEY"],
  kilo: ["KILO_API_KEY"],
  "kimi-for-coding": ["KIMI_API_KEY"],
  klokintegration: ["KLOKINTEGRATION_API_KEY"],
  kosmik: ["KOSMIK_API_KEY"],
  "kuae-cloud-coding-plan": ["KUAE_API_KEY"],
  lilac: ["LILAC_API_KEY"],
  llama: ["LLAMA_API_KEY"],
  llmgateway: ["LLMGATEWAY_API_KEY"],
  "llmgateway-providers": ["LLMGATEWAY_API_KEY"],
  llmtech: ["LLMTECH_API_KEY"],
  llmtr: ["LLMTR_API_KEY"],
  lmstudio: ["LMSTUDIO_API_KEY"],
  longcat: ["LONGCAT_API_KEY"],
  lucidquery: ["LUCIDQUERY_API_KEY"],
  lynkr: ["LYNKR_API_KEY"],
  meganova: ["MEGANOVA_API_KEY"],
  "merge-gateway": ["MERGE_GATEWAY_API_KEY"],
  meta: ["META_MODEL_API_KEY"],
  minimax: ["MINIMAX_API_KEY"],
  "minimax-cn": ["MINIMAX_API_KEY"],
  "minimax-cn-coding-plan": ["MINIMAX_API_KEY"],
  "minimax-coding-plan": ["MINIMAX_API_KEY"],
  mistral: ["MISTRAL_API_KEY"],
  mixlayer: ["MIXLAYER_API_KEY"],
  moark: ["MOARK_API_KEY"],
  modal: ["MODAL_PROXY_TOKEN"],
  "model-oracle-ai": ["MODEL_ORACLE_API_KEY"],
  modelis: ["MODELIS_API_KEY"],
  modelscope: ["MODELSCOPE_API_KEY"],
  moonshotai: ["MOONSHOT_API_KEY"],
  "moonshotai-cn": ["MOONSHOT_API_KEY"],
  morph: ["MORPH_API_KEY"],
  nan: ["NAN_API_KEY"],
  "nano-gpt": ["NANO_GPT_API_KEY"],
  nearai: ["NEARAI_API_KEY"],
  nebius: ["NEBIUS_API_KEY"],
  neon: ["NEON_AI_GATEWAY_BASE_URL", "NEON_AI_GATEWAY_TOKEN"],
  neosmith: ["NEOSMITH_API_KEY"],
  neuralwatt: ["NEURALWATT_API_KEY"],
  nova: ["NOVA_API_KEY"],
  "novita-ai": ["NOVITA_API_KEY"],
  nvidia: ["NVIDIA_API_KEY"],
  ofox: ["OFOX_API_KEY"],
  "ollama-cloud": ["OLLAMA_API_KEY"],
  openai: ["OPENAI_API_KEY"],
  opencode: ["OPENCODE_API_KEY"],
  "opencode-go": ["OPENCODE_API_KEY"],
  openreason: ["OPENREASON_API_KEY"],
  openrouter: ["OPENROUTER_API_KEY"],
  opper: ["OPPER_API_KEY"],
  orcarouter: ["ORCAROUTER_API_KEY"],
  ovhcloud: ["OVHCLOUD_API_KEY"],
  pendra: ["PENDRA_API_KEY"],
  perplexity: ["PERPLEXITY_API_KEY"],
  "perplexity-agent": ["PERPLEXITY_API_KEY"],
  pioneer: ["PIONEER_API_KEY"],
  poe: ["POE_API_KEY"],
  poolside: ["POOLSIDE_API_KEY"],
  "privatemode-ai": ["PRIVATEMODE_API_KEY", "PRIVATEMODE_ENDPOINT"],
  "qihang-ai": ["QIHANG_API_KEY"],
  "qiniu-ai": ["QINIU_API_KEY"],
  qvac: ["QVAC_API_KEY"],
  "regolo-ai": ["REGOLO_API_KEY"],
  requesty: ["REQUESTY_API_KEY"],
  "routing-run": ["ROUTING_RUN_API_KEY"],
  runinfra: ["RUNINFRA_GATEWAY_KEY"],
  sakana: ["SAKANA_API_KEY"],
  "salad-cloud": ["SALAD_CLOUD_API_KEY"],
  "sap-ai-core": ["AICORE_SERVICE_KEY"],
  sarvam: ["SARVAM_API_KEY"],
  scaleway: ["SCALEWAY_API_KEY"],
  "scnet-token-plan": ["SCNET_API_KEY"],
  "scx-ai": ["SCX_API_KEY"],
  sensenova: ["SENSENOVA_API_KEY"],
  siliconflow: ["SILICONFLOW_API_KEY"],
  "siliconflow-cn": ["SILICONFLOW_CN_API_KEY"],
  "snowflake-cortex": ["SNOWFLAKE_ACCOUNT", "SNOWFLAKE_CORTEX_PAT"],
  stackit: ["STACKIT_API_KEY"],
  standardcompute: ["STANDARDCOMPUTE_API_KEY"],
  stepfun: ["STEPFUN_API_KEY"],
  "stepfun-ai": ["STEPFUN_API_KEY"],
  "stepfun-ai-step-plan": ["STEPFUN_API_KEY"],
  "stepfun-step-plan": ["STEPFUN_API_KEY"],
  subconscious: ["SUBCONSCIOUS_API_KEY"],
  submodel: ["SUBMODEL_INSTAGEN_ACCESS_KEY"],
  synthetic: ["SYNTHETIC_API_KEY"],
  "tencent-coding-plan": ["TENCENT_CODING_PLAN_API_KEY"],
  "tencent-token-plan": ["TENCENT_TOKEN_PLAN_API_KEY"],
  "tencent-tokenhub": ["TENCENT_TOKENHUB_API_KEY"],
  tensorx: ["TENSORX_API_KEY"],
  "the-grid-ai": ["THEGRID_API_KEY"],
  thinkingmachines: ["TINKER_API_KEY"],
  tinfoil: ["TINFOIL_API_KEY"],
  togetherai: ["TOGETHER_API_KEY"],
  tokengo: ["TOKENGO_API_KEY"],
  tokenrouter: ["TOKENROUTER_API_KEY"],
  trustedrouter: ["TRUSTEDROUTER_API_KEY"],
  "umans-ai": ["UMANS_AI_API_KEY"],
  "umans-ai-coding-plan": ["UMANS_AI_CODING_PLAN_API_KEY"],
  unorouter: ["UNOROUTER_API_KEY"],
  upstage: ["UPSTAGE_API_KEY"],
  v0: ["V0_API_KEY"],
  vancine: ["VANCINE_API_KEY"],
  venice: ["VENICE_API_KEY"],
  vercel: ["AI_GATEWAY_API_KEY"],
  vivgrid: ["VIVGRID_API_KEY"],
  volcengine: ["ARK_API_KEY"],
  "volcengine-coding-plan": ["ARK_CODING_PLAN_API_KEY"],
  vultr: ["VULTR_API_KEY"],
  "wafer.ai": ["WAFER_API_KEY"],
  wandb: ["WANDB_API_KEY"],
  watsonx: ["WATSONX_AI_APIKEY", "WATSONX_AI_PROJECT_ID"],
  xai: ["XAI_API_KEY"],
  xiaomi: ["XIAOMI_API_KEY"],
  "xiaomi-token-plan-ams": ["XIAOMI_API_KEY"],
  "xiaomi-token-plan-cn": ["XIAOMI_API_KEY"],
  "xiaomi-token-plan-sgp": ["XIAOMI_API_KEY"],
  xpersona: ["XPERSONA_API_KEY"],
  zai: ["ZHIPU_API_KEY"],
  "zai-coding-plan": ["ZHIPU_API_KEY"],
  zeldoc: ["ZELDOC_API_KEY"],
  zenifra: ["ZENIFRA_AI_KEY"],
  zenmux: ["ZENMUX_API_KEY"],
  zhipuai: ["ZHIPU_API_KEY"],
  "zhipuai-coding-plan": ["ZHIPU_API_KEY"],
});

/**
 * The catalog providers whose variables belong to a general-purpose platform
 * account: the forge and the cloud and data platforms. A dispatch that names
 * one is refused (ADR-022 § 17), because the stage would run on a login the
 * operator keeps for other tools, which can be a subscription or OAuth one.
 * A copy of `openCodePlatformProviders` in
 * internal/execution/adapters/opencode_isolation.go.
 */
export const OPENCODE_PLATFORM_PROVIDERS: readonly string[] = Object.freeze([
  "amazon-bedrock",
  "cloudflare-ai-gateway",
  "cloudflare-workers-ai",
  "databricks",
  "digitalocean",
  "github-copilot",
  "gitlab",
  "google-vertex",
  "google-vertex-anthropic",
  "huggingface",
  "snowflake-cortex",
  "vultr",
  "wandb",
]);

/**
 * The OpenCode provider key of a `<provider>/<model>` value: everything before
 * the first slash, the way OpenCode splits `-m`.
 */
export function openCodeProviderKey(model: string): string {
  const slash = model.indexOf("/");
  return slash < 0 ? model : model.slice(0, slash);
}

/** The variables the catalog binds to the provider `model` names, or none. */
export function openCodeProviderEnv(model: string): readonly string[] {
  const key = openCodeProviderKey(model);
  return Object.hasOwn(OPENCODE_CATALOG_ENV, key) ? OPENCODE_CATALOG_ENV[key] : [];
}

/**
 * The provider base-URL variables an opencode dispatch withholds whatever its
 * provider, a copy of `openCodeEndpointEnv` in
 * internal/execution/adapters/opencode_isolation.go (the drift guard in
 * tests/cli/childEnv.test.ts parses it).
 */
export const OPENCODE_ENDPOINT_ENV: readonly string[] = Object.freeze([
  "ANTHROPIC_BASE_URL",
  "OPENAI_BASE_URL",
]);

/** The prefix of OpenCode's own variables, every one of which is withheld. */
export const OPENCODE_WITHHELD_PREFIX = "OPENCODE_";

const OPENCODE_CATALOG_NAMES: ReadonlySet<string> = new Set(
  Object.values(OPENCODE_CATALOG_ENV).flat()
);
const OPENCODE_PLATFORM_NAMES: ReadonlySet<string> = new Set(
  OPENCODE_PLATFORM_PROVIDERS.flatMap((p) => OPENCODE_CATALOG_ENV[p] ?? [])
);

/**
 * Whether an inherited variable named `key` is withheld from an opencode
 * dispatch to `model`: the TS twin of `OpenCodeWithholdsEnv`
 * (opencode_isolation.go). Every `OPENCODE_*` variable, the provider base
 * URLs, and every catalog variable of a model service other than the
 * dispatched one; a platform provider's variables (the forge token, the cloud
 * credentials) never. Decided on the name alone.
 */
export function openCodeWithholdsEnv(model: string, key: string): boolean {
  if (key.startsWith(OPENCODE_WITHHELD_PREFIX) || OPENCODE_ENDPOINT_ENV.includes(key)) return true;
  if (!OPENCODE_CATALOG_NAMES.has(key) || OPENCODE_PLATFORM_NAMES.has(key)) return false;
  return !openCodeProviderEnv(model.trim()).includes(key);
}

/**
 * {@link openCodeWithholdsEnv} for one dispatch, as data: the TS twin of
 * `OpenCodeEnvWithholdFor` (opencode_config.go), the `env_withhold` that
 * `nightgauge opencode config` prints. Names are sorted and unique.
 */
export function openCodeEnvWithholdFor(model: string): {
  prefixes: string[];
  names: string[];
} {
  const names = new Set<string>(OPENCODE_ENDPOINT_ENV);
  for (const name of OPENCODE_CATALOG_NAMES) {
    if (openCodeWithholdsEnv(model, name)) names.add(name);
  }
  return { prefixes: [OPENCODE_WITHHELD_PREFIX], names: [...names].sort() };
}
