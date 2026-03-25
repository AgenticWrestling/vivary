# OpenClaw Migration Design

## Goal

Design a mostly automatic path from OpenClaw into VIVARY that preserves the highest-usage operator value first, keeps VIVARY's governance model intact, and makes non-portable pieces explicit instead of pretending they are equivalent.

The migration target is **not** "run OpenClaw inside VIVARY." The target is:

- import the parts that map cleanly into VIVARY's governed runtime,
- surface the parts that need operator review,
- isolate or defer the parts that conflict with VIVARY's architecture.

The **v0 deliverable** is narrower: a discovery action that inspects an OpenClaw install, reports what it finds, and classifies what is migratable, review-required, bridge-required, or unsupported. v0 does not need to perform live migration yet.

## Why this is hard

OpenClaw is a broad gateway product: chat channels, plugins, skills, slash commands, mobile nodes, provider integrations, and automation. VIVARY is a narrower governed runtime where `keeperd` is the policy authority and `ward` is a syntax adapter.

That means migration cannot be a raw config translation. It needs a semantic import pipeline that:

- inventories what the OpenClaw install actually uses,
- classifies portability by artifact type,
- rewrites safe subsets into VIVARY-native config and prompt assets,
- asks the operator for the few decisions that materially affect security or behavior.

## Principles

- Preserve the `ward`/`keeperd` boundary. No OpenClaw-style dynamic plugin host inside `ward`.
- Prioritize what users most likely depend on daily: prompts/skills, commands, standing automation, channel entry points, and common tools.
- Translate behavior into VIVARY policy where possible rather than copying permissive runtime behavior.
- Keep unsupported items visible with reasons and next actions.
- Default to import with review, not silent enablement.

## Migration Priorities

### P0: First migration slice

These give the highest user-visible continuity with the lowest architectural distortion.

1. **Skills and prompt assets**
   - Import `SKILL.md`-style assets and workspace instructions.
   - Convert them into VIVARY prompt includes or agent-template prompt fragments.
   - Preserve metadata, provenance, and whether the asset was user-invocable.

2. **Slash commands and prompt macros**
   - Convert OpenClaw slash commands into VIVARY operator-side prompt presets/macros.
   - Keep argument placeholders and descriptions.
   - Mark commands that depended on channel-native slash semantics as "interactive macro only."

3. **Standing orders / recurring instructions**
   - Convert standing-order documents and cron-like automation into `keeperd`-scheduled prompts plus approval/rate policies.
   - Keep automation governance in `keeperd`, not in prompt text.

4. **Tool policy mapping for common built-ins**
   - Map common OpenClaw tool usage to VIVARY capabilities where equivalents exist first: browser/web, filesystem, execution, search, messaging, memory.
   - Generate draft `allow` blocks with conservative scope defaults.

5. **Credentials and provider references**
   - Discover referenced API keys/tokens/providers.
   - Import only references and metadata automatically.
   - Require operator mapping into the VIVARY vault before activation.

### P1: Second slice

- Selected plugin bundles that only contribute skills/prompts/commands.
- Selected tool plugins whose behavior can be represented as existing VIVARY capabilities.
- Basic model/provider translation into VIVARY agent templates.
- Optional session/history archive import for operator inspection only.

### P2: Later slice

- Channel migration via a dedicated ingress bridge for Telegram/Discord/WhatsApp-like entry points.
- Message routing compatibility for a single imported assistant identity.
- Limited workflow migration for media-heavy flows.

### P3: Explicitly deferred or likely unsupported

- Native OpenClaw plugins that execute arbitrary in-process code.
- Mobile nodes, macOS node actions, device control, and host-side permissions.
- Broad multi-agent routing parity from OpenClaw.
- Automatic import of raw chat history into active prompt context.

## Portability Tiers

Every discovered artifact is classified into one of four buckets:

- `portable`: can be rewritten into VIVARY-native assets with no semantic loss that matters for MVP.
- `portable_with_review`: can be migrated, but the operator must confirm permissions, scopes, schedules, or secret mapping.
- `bridge_required`: useful, but needs a compatibility sidecar outside the core runtime.
- `unsupported`: conflicts with VIVARY's architecture or trust model.

This classification should be visible in both CLI output and a generated report.

## Proposed UX

Add a dedicated migration flow to `viv`:

```text
viv migrate openclaw inspect [--source ~/.openclaw]
viv migrate openclaw plan    [--from claw-import.json]
viv migrate openclaw apply   [--plan claw-plan.json]
```

For **v0**, only `inspect` is required.

```text
viv migrate openclaw inspect [--source ~/.openclaw]
```

The first shipped experience should be excellent at discovery, inventory, and reporting before any write path exists.

### 1. Inspect

`inspect` scans the OpenClaw installation and produces a normalized inventory:

- `~/.openclaw/openclaw.json`
- plugin declarations and enabled/disabled state
- workspace and global extensions
- skill roots and `SKILL.md` files
- slash command definitions
- standing-order docs and automation references
- provider references and secret placeholders
- channel configuration blocks

Output:

- `claw-import.json` - normalized inventory with provenance
- `claw-report.md` - human-readable summary with portability classifications

This is the **v0 deliverable**.

The report should answer, at minimum:

- what OpenClaw assets were discovered,
- which assets appear actively used versus merely installed,
- which items are likely portable into VIVARY,
- which items need operator review,
- which items would require a bridge,
- which items are unsupported and why.

## OpenClaw State Inventory

The discovery action should explicitly model the different kinds of state that can exist in a typical OpenClaw install. This matters because migration difficulty is mostly a function of state shape: some state is declarative and easy to analyze, some is procedural and needs review, and some is operational residue that should be reported but not imported.

### 1. Declarative config state

This is the highest-value, lowest-ambiguity state to inspect first.

- primary config such as `~/.openclaw/openclaw.json`
- per-channel config blocks
- plugin enablement and plugin config
- model/provider selection and defaults
- command toggles, access controls, allowlists, mention rules
- automation settings, webhook endpoints, poll definitions, auth monitoring settings
- paths for workspaces, extensions, skills, logs, and auxiliary data roots

Discovery should capture:

- raw config paths and last-modified times
- normalized config keys recognized by the inspector
- unknown or custom keys for later review
- whether each config item appears active, defaulted, disabled, or stale

### 2. Credential and secret reference state

OpenClaw installs often contain references to provider keys, bot tokens, OAuth setup, webhook secrets, and per-plugin credentials.

Examples include:

- model provider API keys
- Discord, Slack, Telegram, WhatsApp, Signal, or other channel tokens
- plugin-specific API keys
- webhook signing secrets
- OAuth client IDs and related account bindings
- environment-variable references injected into skills or plugin config

Discovery should distinguish:

- inline secret values,
- env-var references,
- external secret files,
- unresolved required secret references,
- credential usage sites.

The report should never print raw secret values, but it should say what kind of secret exists, where it is referenced, and whether it is needed for migratable functionality.

### 2a. Credential classes observed in OpenClaw docs and plugins

The discovery action should go beyond saying "a secret exists" and classify each credential by kind. Based on the built-in channel, provider, and plugin docs reviewed so far, a typical OpenClaw install may contain the following credential classes.

This list should be treated as a **best-effort complete list of credential kinds**, not a guarantee that every community plugin is covered. The inspector should still preserve an `unknown_credential_kind` bucket for anything it cannot classify.

#### API keys

Used by many model providers and some service plugins.

Examples observed in docs:

- OpenAI API key
- Anthropic API key
- Telnyx API key
- generic provider/plugin API keys

Discovery shape:

- inline config value
- env-var reference
- external auth store reference

Suggested VIVARY class: `api_key`

#### OAuth and OAuth-like tokens

Used by providers and some subscription-backed integrations.

Examples observed in docs:

- OpenAI Codex / ChatGPT sign-in OAuth
- Anthropic Claude setup-token flow
- Qwen device-code OAuth with refreshable tokens
- reused CLI auth stores from provider-specific tooling

Discovery should recognize:

- access token presence
- refresh token presence
- setup/bootstrap token presence
- external login-store reuse paths

Suggested VIVARY classes:

- `oauth_access_token`
- `oauth_refresh_token`
- `setup_token`
- `external_auth_store`

#### Bot tokens and app tokens

Used by messaging platform integrations.

Examples observed in docs:

- Telegram bot token
- Discord bot token
- Slack bot token
- Slack app token for Socket Mode

Suggested VIVARY classes:

- `bot_token`
- `app_token`

#### Client IDs and application identifiers

Some integrations need non-secret identifiers alongside a secret.

Examples observed in docs and setup flows:

- OAuth client IDs
- provider/app identifiers
- telephony connection IDs

These are not always secrets, but they are operational auth material and should be inventoried with credentials because migrations often fail when the identifier is lost.

Suggested VIVARY class: `client_id_or_app_id`

#### Secret pairs and compound credentials

Some plugins authenticate with a tuple rather than a single secret.

Examples observed in docs:

- Twilio `accountSid` + `authToken`
- AWS `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`
- account identifier plus webhook verification material

Suggested VIVARY classes:

- `account_sid_auth_token`
- `access_key_secret_key`
- `compound_credential`

#### Passwords and shared secrets

Some channels or helper services use a classic password/shared-secret model.

Examples observed in docs:

- BlueBubbles API password
- plugin or webhook shared secrets

Suggested VIVARY classes:

- `password`
- `shared_secret`

#### Webhook verification credentials

These are often distinct from the primary API credential.

Examples observed in docs:

- Telegram webhook secret
- Slack signing secret
- Twilio/Plivo webhook verification material
- Telnyx webhook public key / signature verification material

Suggested VIVARY classes:

- `webhook_secret`
- `signing_secret`
- `webhook_public_key`

#### Cloud-provider credential chain material

Some providers do not use a single stored API key and instead rely on a credential chain.

Examples observed in docs:

- Amazon Bedrock via AWS SDK default chain
- `AWS_BEARER_TOKEN_BEDROCK`
- `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`
- `AWS_PROFILE`
- instance role / host IAM identity

These need special handling because the "credential" may be implicit in the host environment rather than stored in OpenClaw config.

Suggested VIVARY classes:

- `cloud_bearer_token`
- `cloud_access_key_pair`
- `cloud_profile_reference`
- `host_role_identity`

#### QR/device-pairing session credentials

Some channels authenticate by linking a device and then persisting session state on disk instead of storing a human-readable token.

Examples observed in docs:

- WhatsApp linked-device credentials in `~/.openclaw/credentials/whatsapp/<accountId>/creds.json`
- QR-login-backed account state for channels like Zalo Personal

This class is important because it is often highly stateful, difficult to migrate safely, and better treated as bridge-only or re-auth-required.

Suggested VIVARY classes:

- `device_pairing_session`
- `qr_login_session`

#### Local credential files and auth-store reuse

OpenClaw can sometimes reuse credentials created by another CLI or local helper.

Examples observed in docs:

- Qwen auth reused from `~/.qwen/oauth_creds.json`
- Claude login/setup-token material reused on the gateway host
- WhatsApp credential directories under `~/.openclaw/credentials/`

These are not credential kinds by themselves, but they are a distinct storage mode that the inspector should report.

Suggested storage-source classes:

- `local_auth_store`
- `imported_cli_login`
- `credential_file`

#### Gateway/control-plane tokens

Some installs also protect remote clients or admin surfaces with gateway-level credentials.

Examples observed in docs:

- `OPENCLAW_GATEWAY_TOKEN` for gateway connection auth

These are not plugin-specific, but they matter for migration because they indicate protected control surfaces and may influence how remote access is re-established in VIVARY.

Suggested VIVARY class: `gateway_access_token`

#### No-credential or implicit-local providers

A few providers or local model backends may run without user-managed credentials.

Examples from provider docs:

- self-hosted OpenAI-compatible endpoints
- local Ollama/SGLang-style backends
- host-role-based cloud auth where no secret is stored in OpenClaw files

The inspector should still record these as auth posture, because "no stored credential" is materially different from "credential missing."

Suggested classes:

- `no_stored_credential`
- `implicit_host_auth`

### 2b. How discovery should report credentials

For each credential reference, `inspect` should emit:

- logical owner: provider, channel, plugin, automation, or gateway surface
- credential class from the taxonomy above
- storage source: inline config, env var, local auth store, credential file, host environment, or inferred external dependency
- scope: global, per-agent, per-account, per-channel, or per-plugin
- secrecy status: secret, identifier-only, mixed pair, or implicit host auth
- migration posture: portable reference, portable with review, bridge required, re-auth required, or unsupported
- redacted locator: enough to identify the source without exposing the secret

Examples:

- `channel:telegram/default` -> `bot_token` via env var `TELEGRAM_BOT_TOKEN`
- `channel:slack/default` -> `app_token` + `bot_token` via config
- `provider:anthropic/default` -> `api_key` or `setup_token` via gateway-host auth store
- `provider:amazon-bedrock/default` -> `host_role_identity` or `cloud_profile_reference`
- `plugin:voice-call` -> `account_sid_auth_token` for Twilio, `api_key` for Telnyx, `webhook_public_key` for webhook verification
- `channel:whatsapp/work` -> `device_pairing_session` via credential file

### 2c. Migration implications of credential kinds

Credential class should directly affect migration recommendations.

- `api_key`, `bot_token`, `password`, `shared_secret`, `signing_secret`: usually migratable as a vault reference, never copied into prompt assets.
- `oauth_access_token`, `oauth_refresh_token`, `setup_token`, `external_auth_store`: often require re-auth on the VIVARY host or an explicit import flow.
- `device_pairing_session`, `qr_login_session`: usually report-only or bridge-required; default to re-auth instead of silent import.
- `cloud_profile_reference`, `host_role_identity`, `implicit_host_auth`: migrate as environment/infrastructure requirements, not vault entries.
- `client_id_or_app_id`: preserve even if non-secret, because it is often needed to recreate the full auth binding.
- `no_stored_credential`: report as valid auth posture, not as a missing secret.

### 2d. Credentials seen in popular community plugins

A GitHub sweep of higher-star OpenClaw community plugins/connectors suggests that the most common credential patterns in the wild are not evenly distributed. The ecosystem is heavily skewed toward channel/connectivity plugins, and those bring their own auth patterns.

Most visible repos found in the sweep included:

- DingTalk connectors: `DingTalk-Real-AI/dingtalk-openclaw-connector`, `soimy/openclaw-channel-dingtalk`
- WeChat / WeCom connectors: `freestylefly/openclaw-wechat`, `HenryXiaoYang/wechat-openclaw-channel`, `sunnoy/openclaw-plugin-wecom`, `WecomTeam/wecom-openclaw-plugin`, `11haonb/wecom-openclaw-plugin`
- QQ / chat bridges: `tencent-connect/openclaw-qqbot`, `izhimu/openclaw-channel-qq`, `CharTyr/napcat-plugin-openclaw`, `laozuzhen/xianyu-openclaw-channel`
- Memory plugins: `MemTensor/MemOS-Cloud-OpenClaw-Plugin`
- Search / host bridges: `zhao-xuxu/openclaw-websearch-plugin`, `TomLeeLive/openclaw-unity-plugin`, `Skyzi000/openclaw-open-webui-channels`
- Local knowledge/vault plugin: `pepicrft/openclaw-plugin-vault`

The sweep is noisy and GitHub search is not a canonical registry, but it is still useful for seeing what credential classes are common in practice.

Observed recurring credential patterns from those repos:

- `bot_token` / `app_token` / channel access tokens
- `webhook_secret` / `signing_secret`
- `api_key` for proprietary channel bridges and search services
- `oauth_access_token` or OAuth-style app credentials for enterprise/chat platforms
- `cookie` / browser-session or login-session style credentials in some bridge plugins
- `qr_login_session` and interactive login flows in personal-account channel connectors
- `local_host` or implicit local-service trust for plugins that bridge to Unity, Open WebUI, local vaults, or local agents

Discovery implication:

- community plugin credential handling is often less standardized than built-in providers,
- many plugins appear to rely on bridge-specific auth or session state,
- the inspector should preserve both a normalized credential class and the raw config field names that revealed it.

## Popular Community Plugin Categories

The current GitHub ecosystem suggests a fairly clear ranking of what community plugin authors build most often.

### 1. Channel connectors

This is the dominant category by far.

Common targets found in higher-star repos:

- DingTalk
- WeChat
- WeCom
- QQ
- Xianyu / Goofish
- NapCat-based chat bridges

Why it matters:

- these plugins represent user demand for message ingress/egress,
- they are usually operationally important,
- but they are also the least aligned with VIVARY's current core runtime boundary.

Likely auth patterns:

- tokens
- app credentials
- webhook secrets
- QR login state
- cookies/session files

Default migration posture: `bridge_required`

### 2. Memory plugins

The second clearest category is long-term memory augmentation.

Examples found:

- MemOS / cloud memory plugins
- LanceDB-style memory plugins from prior ecosystem scans

Why it matters:

- these plugins often hold durable user/agent context,
- they may be high-value for migration,
- but they usually depend on an external memory backend and plugin lifecycle hooks.

Likely auth patterns:

- API keys
- bearer tokens
- external memory-service account credentials

Default migration posture: `portable_with_review` if the memory backend can be modeled as a VIVARY capability or archived as data; otherwise `unsupported` until a first-class memory gateway exists.

### 3. Search and web augmentation plugins

These extend OpenClaw with stronger search, page fetching, or structured web retrieval.

Examples found:

- `zhao-xuxu/openclaw-websearch-plugin`

Likely auth patterns:

- search API keys
- service tokens
- optional local browser/session credentials

Default migration posture: `portable_with_review` when reducible to VIVARY `Search_*` / `Browser_*` capabilities.

### 4. Host-app and local bridge plugins

These connect OpenClaw to a local process or app rather than an external SaaS.

Examples found:

- Unity bridge
- Open WebUI channel bridge
- local vault/knowledge directory plugin

Likely auth patterns:

- local HTTP trust
- localhost endpoints
- optional API key/shared secret
- filesystem path allowlists rather than classic credentials

Default migration posture: usually `unsupported` in-core, occasionally `portable` as prompt/data import, or `bridge_required` if a clean sidecar model is possible.

### 5. Developer workflow / agent bridge plugins

Some plugins bridge OpenClaw to other agent tools or developer runtimes.

Examples found:

- Claude Code-related plugin
- Open WebUI bridge

Likely auth patterns:

- reused local auth stores
- OAuth tokens from another tool
- local session files

Default migration posture: mostly `unsupported` or `bridge_required` because they tend to reintroduce a dynamic agent host inside the system.

## Migration Priority Table For Popular Plugin Classes

Based on popularity and architectural fit, VIVARY should not prioritize plugin classes purely by GitHub stars. It should prioritize by **user value multiplied by architectural compatibility**.

| Plugin class | Popularity signal | User value | Architectural fit | Default posture | Suggested priority |
|---|---|---|---|---|---|
| Prompt/skill bundles | high ecosystem usage, though not always surfaced as plugins | high | high | `portable` | P0 |
| Search/web augmentation | moderate | medium-high | medium-high | `portable_with_review` | P1 |
| Memory plugins | moderate and strategically important | high | medium | `portable_with_review` | P1 |
| Channel connectors | very high | high | low in core runtime | `bridge_required` | P2 |
| Local vault/knowledge plugins | niche but useful | medium | medium | `portable_with_review` or `portable` for file import | P1 |
| Host-app bridges (Unity/Open WebUI/etc.) | niche | medium | low | `bridge_required` or `unsupported` | P3 |
| Agent-to-agent/dev-runtime bridges | niche | low-medium | low | `unsupported` | P3 |
| Arbitrary-code native plugins | unknown | varies | very low | `unsupported` | P3 |

Recommended interpretation:

- **Do not chase channel connectors first just because they are popular.** They are important, but they should land as bridges, not as runtime-core features.
- **Do prioritize prompt, memory, search, and knowledge import.** Those categories preserve real user value while fitting the VIVARY model.
- **Treat community plugin popularity as discovery input, not as a direct roadmap signal.** High-star plugins often reflect demand for ingress convenience, while VIVARY's first priority is governed runtime integrity.

## Non-Chat Plugin Pass By VIVARY Capability Category

A second GitHub pass looked specifically for **non-chat** OpenClaw plugins and compared them against VIVARY's capability categories in `capabilities/categories.kdl`.

The strongest signal is that the non-chat plugin ecosystem is **not evenly distributed** across VIVARY's categories. A few categories have meaningful evidence of community demand; many others have little visible plugin activity.

### Strong evidence categories

#### Memory

This is the clearest non-chat plugin category by far.

Higher-visibility examples found:

- `CortexReach/memory-lancedb-pro`
- `oceanbase/powermem`
- `nhevers/MoltBrain`
- `MemTensor/MemOS-Cloud-OpenClaw-Plugin`
- `legendaryvibecoder/gigabrain`
- `Shubhamsaboo/openclaw-vertexai-memorybank`

Relevant VIVARY category:

- `Memory`

Common patterns:

- vector or hybrid retrieval backends
- local-first memory stores or cloud memory APIs
- ingestion on run end, retrieval before run start
- API-key or token-based auth, sometimes self-hosted/no-auth modes

Migration implication:

- this is a real ecosystem demand signal,
- and it aligns relatively well with a future `memory-gateway` model.

Suggested posture: `portable_with_review`, high future value.

#### Search

Search is the next most visible non-chat category.

Examples found:

- `framix-team/openclaw-tavily`
- `binglius/claw-search`
- `keith-vs-kev/searxng-search`
- `5p00kyy/openclaw-plugin-searxng`

Relevant VIVARY category:

- `Search`

Common patterns:

- hosted search APIs such as Tavily
- self-hosted SearXNG-style search backends
- low-friction wrappers around web retrieval

Migration implication:

- strong fit for VIVARY's `Search` namespace,
- usually simpler than channel migration,
- credentials are often `api_key` or `no_stored_credential` for self-hosted engines.

Suggested posture: `portable_with_review`, near-term candidate.

#### Filesystem / local knowledge vault

This category shows up, but less often than memory/search.

Examples found:

- `pepicrft/openclaw-plugin-vault`
- `timotme/openclaw-telegram-chat-file-browser` (mixed chat/filesystem case)

Relevant VIVARY categories:

- `Filesystem`
- `Document`
- partly `Memory`

Common patterns:

- local markdown vaults
- structured note directories
- path-based access rather than remote API auth

Migration implication:

- often better modeled as imported files plus path-scoped `Filesystem_*` and `Document_*` capabilities,
- may not need plugin parity at all.

Suggested posture: `portable` or `portable_with_review`.

### Moderate / emerging evidence categories

#### Browser / web automation

Visible, but weaker and noisier than memory/search.

Examples found:

- `redf0x1/camofox-browser`

Relevant VIVARY category:

- `Browser`

Common patterns:

- external browser servers
- anti-detection wrappers
- browser-as-a-service style bridges

Migration implication:

- user need exists,
- but many plugins do not match VIVARY's host-side Chrome proxy model cleanly.

Suggested posture: usually `bridge_required` unless the behavior maps onto existing `Browser_*` capabilities.

#### System / execution safety

Not many hits, but enough to matter.

Examples found:

- `knostic/openclaw-shield`

Relevant VIVARY categories:

- `Execution`
- `System`

Common patterns:

- preventing destructive commands
- blocking secret leakage or risky execution
- policy wrappers around existing tools rather than net-new business functionality

Migration implication:

- this is a sign that OpenClaw users want governance and guardrails,
- which overlaps directly with VIVARY's core value proposition.

Suggested posture: not a plugin to port directly, but strong product evidence to strengthen policy, approvals, and denial UX in VIVARY.

### Weak or little visible evidence in the public plugin ecosystem

The GitHub pass found little high-visibility plugin evidence for these VIVARY categories:

- `Calendar`
- `Spreadsheet`
- `Document` as a standalone remote-provider plugin category
- `Database`
- `Email`
- `VersionControl`
- `Media`
- `Commerce`
- `SmartHome`
- `Swarm`

This does **not** mean users do not want those capabilities. It more likely means:

- they are served by built-ins rather than community plugins,
- they are folded into broader provider plugins,
- or the public GitHub plugin ecosystem is currently concentrated elsewhere.

Discovery implication:

- `inspect` should not infer low user importance from low plugin visibility,
- but it should use ecosystem evidence to prioritize where compatibility shims are most likely to matter.

## Non-Chat Plugin Conclusions

If we filter out chat connectors and look only at plugin demand that resembles VIVARY capability namespaces, the ranking currently looks roughly like this:

1. `Memory`
2. `Search`
3. `Filesystem` / local knowledge vaults
4. `Browser`
5. `System` / execution safety wrappers
6. everything else with weak public plugin evidence

That ranking is useful because it is much closer to VIVARY's architecture than the raw "most starred OpenClaw plugins" list.

Recommended consequence for the discovery design:

- explicitly tag plugins by nearest VIVARY category,
- detect `Memory` and `Search` plugins as high-interest non-chat migration candidates,
- treat local vault/file plugins as likely importable data sources,
- treat browser bridges cautiously,
- and avoid over-weighting the absence of public plugins for categories like `Calendar`, `Spreadsheet`, or `Database`.

### 3. Prompt and instruction state

A typical OpenClaw install accumulates a large amount of behavior in prompt-like assets rather than only in config.

This includes:

- `SKILL.md` files
- bundled and local skill folders
- custom system-prompt fragments
- slash-command prompt templates
- standing-order documents
- workspace instruction files
- plugin-shipped prompt packs and skill bundles

Discovery should record:

- asset path and origin
- frontmatter metadata
- whether the asset is bundled, workspace-local, globally installed, or plugin-provided
- whether it appears user-invocable, model-invocable, or automation-only
- referenced tools, secrets, and external binaries
- token-size rough estimate for operator awareness

### 4. Tool and capability state

OpenClaw behavior depends heavily on which tools are available and how they are surfaced to the model.

Typical state includes:

- built-in tool availability
- plugin-registered tools
- skill-to-tool dependencies
- approval settings for exec/elevated behavior
- browser, web, exec, diff, patch, PDF, search, and message tool configuration
- sandboxing and risky-tool toggles

Discovery should try to answer:

- which tools are merely available,
- which tools are referenced by skills/commands,
- which tools appear frequently used,
- which tools have likely VIVARY capability mappings,
- which tools imply host-trust or dynamic code execution.

### 5. Plugin and extension state

Plugins are one of the most important state categories because they are also the least portable.

The inspector should inventory:

- installed plugins
- enabled vs disabled plugins
- bundled vs external plugins
- local workspace extensions
- bundle-style plugin layouts
- plugin config schemas when discoverable
- plugin-provided channels, tools, skills, providers, speech, image, or automation features

For each plugin, the report should say:

- what capability class it belongs to,
- whether it contributes portable assets only,
- whether it runs arbitrary code,
- whether it appears currently in use,
- whether it is likely `portable`, `portable_with_review`, `bridge_required`, or `unsupported`.

### 6. Channel and identity state

OpenClaw is frequently used as a chat gateway, so channel state is often central to a real install.

Typical state includes:

- configured channels and channel-specific settings
- bot identities, phone numbers, workspace IDs, guild IDs, or chat routing rules
- pairing state and allowlists
- group activation and mention rules
- session-keying behavior by channel
- broadcast groups or routing groups

Discovery should separate:

- configured channels,
- channels that appear active,
- channels that likely need a bridge,
- channels that depend on unsupported local/mobile/macOS components.

### 7. Memory and session state

OpenClaw may maintain both short-lived conversation state and longer-lived memory/state artifacts.

This can include:

- per-session conversation transcripts
- session metadata and session keys
- direct-message vs group-chat session partitioning
- long-term memory stores or summaries
- compaction/pruning state
- bookmarks, saved context, or workflow residue

For VIVARY, this state should usually be treated as **inspectable archive state**, not automatically imported live state.

Discovery should report:

- what session and memory stores exist,
- approximate size/count,
- recency,
- whether the data looks exportable,
- whether it should be archived, sampled, or ignored.

### 8. Automation state

Automation is more than one config block; it is often spread across prompts, schedules, webhooks, and plugin behavior.

State to inspect:

- standing orders
- cron jobs
- heartbeat-style automation
- incoming webhooks
- Gmail PubSub or similar event subscriptions
- polls and auth-monitoring jobs
- retry and escalation instructions embedded in prompt assets

Discovery should identify both:

- explicit scheduler state, and
- implicit automation hidden inside prompt text.

That distinction matters because explicit schedules are easier to migrate than free-form instructions like "check every morning and report."

### 9. File and workspace state

Some OpenClaw installs are centered around one or more workspaces with local files, extension code, generated artifacts, and cached media.

This state can include:

- configured workspace roots
- project-local `.openclaw/` directories
- extension source files
- local skill folders
- prompt assets checked into repos
- uploaded/downloaded media caches
- generated outputs and temp files
- logs and diagnostic bundles

Discovery should distinguish:

- operator-authored files,
- generated caches,
- transient temp data,
- files referenced by active config,
- files that likely matter for migration versus files that are just residue.

### 10. Runtime and operational state

Some important state only exists because the gateway has been running.

Examples:

- current daemon/process status
- active sessions or active channel connections
- recent errors and health indicators
- restart-required config drift
- discovered plugins that are configured but missing on disk
- platform-specific dependencies or broken integrations

This state may not be migrated directly, but it is essential for giving the operator an accurate report about install health and likely migration risk.

### 11. Platform-coupled state

Certain OpenClaw features are tied to specific operating systems, devices, or host permissions.

Examples:

- macOS node permissions
- BlueBubbles/iMessage dependencies
- camera/screen/location/voice node integrations
- mobile pairing state
- local notification permissions
- elevated host command permissions

These should be treated as a distinct category because they are rarely portable into VIVARY's core runtime. The report should call them out clearly instead of letting them hide inside generic plugin or channel summaries.

## Discovery Report Structure

To make the v0 deliverable useful, the report should have explicit sections for each major state class:

1. install overview
2. declarative config
3. credentials and secret references
4. prompt/skill assets
5. tools and capability mappings
6. plugins and extensions
7. channels and identities
8. memory and session stores
9. automation and schedules
10. workspace/files/media/logs
11. platform-coupled state
12. migration summary by portability tier

For each section, the report should include counts, notable findings, migration confidence, and operator actions needed.

### 2. Plan

`plan` turns the raw inventory into a concrete migration plan.

It should:

- infer candidate VIVARY agent templates,
- map tools/plugins to capabilities,
- draft policy scopes and approval gates,
- mark unsupported items,
- build a question set for operator decisions.

Output:

- `claw-plan.json` - machine-readable migration plan
- `claw-questions.json` - bounded operator decision set

### 3. Apply

`apply` writes VIVARY-native artifacts, but leaves them disabled until review is complete.

Expected outputs:

- imported prompt fragments
- imported macro/command definitions
- draft agent template or agent config files
- vault import manifest with unresolved secret mappings
- schedule definitions for standing orders
- migration audit log

## Import Pipeline Architecture

Use a three-stage design.

### Stage A: Source Adapters

Source adapters only parse OpenClaw assets into a neutral format. They do not make policy decisions.

Adapters:

- config adapter
- skills adapter
- commands adapter
- plugins adapter
- automation adapter
- channels adapter

### Stage B: Normalized Migration Manifest

Create a VIVARY-owned intermediate format, for example:

- `ClawInstall`
- `ClawAgentProfile`
- `ClawSkill`
- `ClawCommand`
- `ClawAutomation`
- `ClawPlugin`
- `ClawChannel`
- `ClawCredentialRef`

Every object carries:

- source path
- source type
- confidence score
- portability tier
- suggested VIVARY target
- warnings

This manifest is the key to making the migration deterministic, testable, and debuggable.

### Stage C: VIVARY Translators

Translators consume the manifest and emit VIVARY-native artifacts only:

- prompt fragments
- capability grants
- schedule/rate/approval policies
- vault references
- ingress bridge config when needed

No translated output should require runtime plugin loading.

## Artifact Mapping

### Skills

OpenClaw skills are the easiest win.

Mapping:

- `SKILL.md` frontmatter -> imported metadata
- body -> VIVARY prompt include
- `user-invocable` -> candidate macro exposure in `viv`
- environment injection requests -> vault mapping questions

Rules:

- import prompt text automatically,
- strip or flag OpenClaw-only directives,
- preserve original asset alongside translated output,
- add provenance comments/metadata so operators can audit the source.

### Slash Commands

Map each slash command to a VIVARY macro that expands into either:

- a prompt preset,
- a ctl action plus prompt,
- or a manual-only command if it depended on chat-platform slash plumbing.

Commands that used OpenClaw-specific session naming should be rewritten against VIVARY's prompt/session model or marked for manual review.

### Standing Orders and Automation

These should become first-class `keeperd`-managed schedules, not just imported prompt text.

Mapping:

- trigger cadence -> schedule blocks
- execution limits -> rate blocks
- escalation rules -> approval requirements + denial behavior
- program instructions -> prompt payload/template

This is a strong fit for VIVARY because governance belongs in `keeperd`.

### Tool and Plugin Mapping

Plugins need triage.

1. **Prompt-only / bundle-style plugins**
   - Import as skills, commands, or prompt packs.
   - Usually `portable` or `portable_with_review`.

2. **Tool plugins with behavior covered by VIVARY capabilities**
   - Translate usage into capability grants.
   - Example: browser, exec, filesystem, search, messaging.
   - Usually `portable_with_review`.

3. **Provider plugins**
   - Import provider metadata and credential references only.
   - Map to VIVARY provider/CLI configuration if supported.
   - Usually `portable_with_review`.

4. **Channel plugins**
   - Do not import into the runtime core.
   - Route through an ingress bridge.
   - Usually `bridge_required`.

5. **Native arbitrary-code plugins**
   - Mark unsupported unless reimplemented as VIVARY gateways/capabilities.
   - Usually `unsupported`.

## Channel Strategy

OpenClaw's biggest user-facing feature is messaging-channel access, but importing the entire gateway model into VIVARY would blur the MVP boundary.

So the recommended design is:

- keep channel migration **outside** `ward` and `keeperd` core logic,
- add a thin **ingress bridge** that converts inbound channel events into ctl prompts,
- keep the bridge stateless where possible,
- let `keeperd` remain the authority for identity, approvals, logging, and dispatch.

This preserves VIVARY's architecture while still giving a path to Telegram/Discord/WhatsApp continuity later.

For first delivery, channel config should be imported as:

- inventory,
- portability classification,
- bridge-ready config stubs,
- not active runtime behavior.

## Operator Interaction Points

The migration should be mostly automatic, but a few decisions must remain explicit.

### Required questions

1. Which imported assistant profiles should become VIVARY agents?
2. Which secrets map to which VIVARY vault credential IDs?
3. Which risky capabilities should require approval by default?
4. Which channel integrations should be deferred, bridged, or dropped?
5. Which unsupported plugins should be preserved as documentation-only artifacts?

### Good defaults

- default imported capabilities to least privilege,
- default write/send/exec actions to approval-required,
- default channel plugins to deferred,
- default unknown plugins to unsupported,
- default imported automations to disabled until reviewed.

## Generated Outputs

An apply step should create a dedicated migration area such as:

```text
imports/openclaw/
  inventory/claw-import.json
  plans/claw-plan.json
  reports/claw-report.md
  prompts/
  commands/
  schedules/
  bridge/
  originals/
```

This keeps imported assets inspectable and reversible.

For **v0**, only the inventory and report outputs are required. `plan` and `apply` remain future phases.

## Failure Handling

Migration needs first-class partial success behavior.

- If an artifact cannot be translated, keep the original and explain why.
- If a secret is missing, write a blocked placeholder instead of dropping the dependent artifact.
- If a plugin is unsupported, preserve its metadata and source location in the report.
- If a capability mapping is uncertain, emit a draft deny-by-default config plus a review warning.

## Security Notes

- Never execute imported OpenClaw plugin code during migration.
- Never auto-enable imported write/send/exec permissions without review.
- Never copy raw secrets into generated prompt files.
- Treat imported skills and prompt packs as untrusted text until reviewed.
- Keep all imported provenance so operators can trace every generated artifact back to source.

## Recommended Implementation Order

1. Build `inspect` with strong inventory/reporting.
2. Add the normalized manifest and portability classifier.
3. Mark v0 complete once discovery is reliable and the report is useful for operators.
4. Implement skill import.
5. Implement slash command import.
6. Implement standing-order to schedule translation.
7. Implement credential reference import and vault mapping prompts.
8. Implement common tool-to-capability mapping.
9. Add bridge config generation for channels.
10. Add targeted support for selected bundle/plugin types.

## Test Strategy

The importer should have deterministic fixture-based tests.

- unit tests for each source adapter
- unit tests for portability classification
- translator tests for skills, commands, schedules, and credentials
- denial-path tests for unsupported plugins and ambiguous mappings
- integration tests that run `inspect -> plan -> apply` on sample OpenClaw installs
- golden-file tests for generated reports and VIVARY artifacts

## Open Questions

1. Should the first shipped migration include a channel ingress bridge, or should channel continuity be report-only until the single-agent runtime is more mature?
2. Do we want imported slash commands to live only in `viv`, or also appear as agent-visible prompt presets?
3. Should session/history import exist at all, or only as an audit archive outside active runtime state?

## Recommendation

Start with a migration that is excellent at importing **skills, commands, standing orders, provider references, and common tool policy**. That captures a large share of the practical value OpenClaw users rely on, fits VIVARY's current architecture, and avoids prematurely turning VIVARY into a general-purpose plugin gateway.

Treat channels and non-trivial plugins as a second system: inventory them well, generate bridge stubs where sensible, and only implement live compatibility once the runtime core remains legible.

More concretely: ship **discovery first**. A high-quality discovery/reporting action gives immediate value, lowers migration risk, and creates the manifest and classification machinery that every later import step depends on.
