# Migration TUI Implementation Plan

## Goal

Turn `migrate/MIGRATEUX.md` into a buildable Bubble Tea implementation plan for `viv`.

This document focuses on:

- phased delivery,
- state transitions,
- data model shape,
- separation between discovery, planning, and rendering.

## Delivery Strategy

Build the final migration UX in four phases.

### Phase 1: Discovery Review TUI

Ship a read-only TUI around the existing v0 discovery command.

Scope:

- launch discovery from `viv migrate openclaw inspect`
- show scan progress
- show findings overview
- browse artifacts, channels, plugins, and credentials
- write `findings.kdl` and `report.md`

Why first:

- lowest risk
- immediately useful
- validates layout and data model before planning/apply exists

### Phase 2: Plan Builder

Add decision capture and plan generation.

Scope:

- review buckets
- credential resolution actions
- capability/approval defaults
- bridge/defer decisions
- write `plan.kdl`

Why second:

- this is the trust-critical step
- it can remain dry-run-only initially

### Phase 3: Apply Preview

Add plan preview and file-operation preview.

Scope:

- destination summaries
- diff/provenance drawers
- dry-run apply
- backup and overwrite checks

Why third:

- operators can verify exactly what will be written before live writes happen

### Phase 4: Live Apply and Post-Migration Actions

Scope:

- write imported prompt/macros/schedules/policies
- leave risky items disabled by default
- surface blocked follow-up steps

Why last:

- depends on confidence in discovery and planning
- introduces irreversible operator-visible outputs

## Command Shape

Recommended command family:

```text
viv migrate openclaw inspect
viv migrate openclaw tui
viv migrate openclaw plan --from migrate/findings.kdl
viv migrate openclaw apply --plan migrate/plan.kdl
```

Recommended implementation order:

- keep `inspect` as the underlying engine,
- add `tui` as the interactive wrapper,
- let `plan` and `apply` remain scriptable non-TUI entrypoints.

## Architecture

Separate the migration system into three layers.

### 1. Engine layer

Pure Go package, no TUI concepts.

Responsibilities:

- inspect OpenClaw installs
- load findings from KDL
- build migration plans from findings + operator decisions
- render KDL/markdown outputs
- compute previews and summaries
- infer scope hints from imported config and plugin metadata

Suggested packages:

- `internal/migrate` for engine code
- maybe `internal/migrate/kdlio.go`
- maybe `internal/migrate/planner.go`
- maybe `internal/migrate/scopeinfer.go`

### 2. Session layer

Mutable migration session state used by the UI.

Responsibilities:

- current step
- current selection/filter
- accumulated operator decisions
- derived counters and warnings
- dirty state and save checkpoints

Suggested location:

- `cmd/viv/migrate_tui_state.go`

### 3. View layer

Bubble Tea rendering only.

Responsibilities:

- layout shell
- step rendering
- keyboard handling
- drawers/modals
- status footer and flash messages

Suggested location:

- `cmd/viv/migrate_tui.go`
- `cmd/viv/migrate_view_*.go`

## State Machine

The migration wizard should be modeled as an explicit finite-state flow.

```text
Idle
  -> SourceSelect
  -> Discovering
  -> FindingsOverview
  -> ReviewBuckets
  -> CredentialsReview
  -> CapabilityReview
  -> BridgeReview
  -> PlanPreview
  -> ApplyPreview
  -> Applying
  -> Results
  -> Exit
```

### Allowed transitions

- `SourceSelect -> Discovering`
- `Discovering -> FindingsOverview`
- `FindingsOverview -> Exit`
- `FindingsOverview -> ReviewBuckets`
- `ReviewBuckets <-> CredentialsReview`
- `CredentialsReview <-> CapabilityReview`
- `CapabilityReview <-> BridgeReview`
- `BridgeReview -> PlanPreview`
- `PlanPreview -> ApplyPreview`
- `ApplyPreview -> Applying`
- `Applying -> Results`
- `Results -> Exit`

### Escape hatches

The operator can safely exit at:

- `FindingsOverview`
- `PlanPreview`
- `Results`

The operator can also go back from every review step without losing prior decisions.

## Session Data Model

Suggested top-level session struct:

```go
type migrateSession struct {
    SourcePath        string
    OutputDir         string
    Mode              migrateMode
    Step              migrateStep
    Discovering       bool
    Applying          bool
    Dirty             bool
    DryRun            bool

    Findings          *migrate.Discovery
    Plan              *migrate.Plan
    Decisions         operatorDecisions

    SelectedBucket    portabilityBucket
    SelectedListIndex int
    ActiveDrawer      drawerKind
    Flash             string
    LastErr           error
}
```

### Enumerations

```go
type migrateMode string

const (
    migrateModeInspect migrateMode = "inspect"
    migrateModePlan    migrateMode = "plan"
    migrateModeApply   migrateMode = "apply"
)

type migrateStep int

const (
    stepSourceSelect migrateStep = iota
    stepDiscovering
    stepFindingsOverview
    stepReviewBuckets
    stepCredentialsReview
    stepCapabilityReview
    stepBridgeReview
    stepPlanPreview
    stepApplyPreview
    stepApplying
    stepResults
)

type portabilityBucket string

const (
    bucketPortable           portabilityBucket = "portable"
    bucketPortableWithReview portabilityBucket = "portable_with_review"
    bucketBridgeRequired     portabilityBucket = "bridge_required"
    bucketUnsupported        portabilityBucket = "unsupported"
)

type drawerKind string

const (
    drawerNone       drawerKind = "none"
    drawerProvenance drawerKind = "provenance"
    drawerDiff       drawerKind = "diff"
    drawerHelp       drawerKind = "help"
)
```

## Findings View Model

Do not render directly from raw engine structs. Build light view models.

```go
type findingsSummaryVM struct {
    SourcePath            string
    ConfigParsed          bool
    TotalFindings         int
    PortableCount         int
    ReviewCount           int
    BridgeCount           int
    UnsupportedCount      int
    TopOpportunities      []string
    HighestRiskItems      []string
    UnknownConfigFields   []string
}

type findingRowVM struct {
    Kind        string
    Label        string
    SourcePath   string
    TargetPath   string
    Portability  portabilityBucket
    Reason       string
    Selected     bool
    DefaultAction string
}
```

Why:

- rendering code stays simple,
- target paths and recommended actions can be computed once,
- tests can assert UI summaries without duplicating business logic.

## Operator Decision Model

Decisions should be explicit and serializable so the plan builder can run without the TUI.

```go
type operatorDecisions struct {
    ApprovedArtifacts      map[string]bool
    DeferredArtifacts      map[string]bool
    CredentialResolutions  map[string]credentialResolution
    CapabilityPolicies     map[string]capabilityDecision
    BridgeDecisions        map[string]bridgeDecision
    UnsupportedPolicies    map[string]unsupportedDecision
}

type credentialResolution struct {
    Action          string // map | replace | reauth | relink | placeholder
    VaultCredential string
    Notes           string
}

type capabilityDecision struct {
    Enabled          bool
    Scope            string
    ApprovalRequired bool
}

type bridgeDecision struct {
    Action string // defer | generate_stub | drop
}

type unsupportedDecision struct {
    Action string // preserve_report_only | ignore
}
```

## Planner Model

The planner should output a first-class plan type, not ad hoc maps.

```go
type Plan struct {
    SourcePath      string
    GeneratedAt     time.Time
    Summary         PlanSummary
    Writes          []PlannedWrite
    Blocks          []PlanBlocker
    Imports         []PlannedImport
    PreservedItems  []PreservedItem
}

type PlanSummary struct {
    PromptFiles      int
    MacroFiles       int
    ScheduleFiles    int
    PolicyFiles      int
    BridgeStubFiles  int
    BlockedItems     int
}

type PlannedWrite struct {
    Op         string
    Path       string
    SourceRef  string
    SafeByDefault bool
}

type PlannedImport struct {
    Kind        string
    SourceRef   string
    TargetPath  string
    Enabled     bool
    Reason      string
}

type PlanBlocker struct {
    Ref         string
    Severity    string
    Reason      string
    NextAction  string
}
```

### Scope hint model

To close the gap between coarse plugin classification and useful VIVARY policy proposals, discovery/planning should eventually carry scope hints explicitly.

```go
type ScopeHint struct {
    Ref         string
    Category    string
    Entity      string
    Field       string
    Value       string
    Confidence  string
    Reason      string
}
```

Examples:

- search plugin -> `Entity=Link`, `Field=url-prefix`, `Value=https://docs.example.com/*`
- vault plugin -> `Entity=Folder`, `Field=path-prefix`, `Value=workspace:/vault/*`
- memory backend -> `Entity=MemoryRecord`, `Field=source-id`, `Value=mem0-prod`

## Bubble Tea Message Model

Keep long-running work in commands/messages rather than direct mutation.

Suggested messages:

```go
type discoverStartedMsg struct{}
type discoverProgressMsg struct {
    Phase    string
    Percent  int
    Recent   []string
}
type discoverCompleteMsg struct {
    Findings migrate.Discovery
}
type discoverFailedMsg struct { Err error }

type planBuiltMsg struct { Plan migrate.Plan }
type planFailedMsg struct { Err error }

type applyStartedMsg struct{}
type applyProgressMsg struct {
    CurrentOp string
    Done      int
    Total     int
}
type applyCompleteMsg struct { Result applyResult }
type applyFailedMsg struct { Err error }
```

This keeps the UI responsive and easy to test.

## Rendering Breakdown

Each major screen should have its own renderer.

Suggested functions:

```go
func renderSourceSelect(m migrateModel) string
func renderDiscovering(m migrateModel) string
func renderFindingsOverview(m migrateModel) string
func renderReviewBuckets(m migrateModel) string
func renderCredentialsReview(m migrateModel) string
func renderCapabilityReview(m migrateModel) string
func renderBridgeReview(m migrateModel) string
func renderPlanPreview(m migrateModel) string
func renderApplyPreview(m migrateModel) string
func renderApplying(m migrateModel) string
func renderResults(m migrateModel) string
```

The outer shell renderer should compose:

- header
- step rail
- main content
- optional drawer
- footer/status line

## Keyboard Model

Recommended global keys:

- `tab` / `shift-tab` move between major regions
- `j` / `k` move within lists
- `enter` open selected item or advance
- `space` toggle selection
- `a` apply safe bulk action for current screen
- `d` defer selected item
- `p` open provenance drawer
- `/` filter current list
- `?` open help
- `esc` close drawer or go back

Recommended screen-specific keys:

- credentials screen: `m` map, `r` re-auth, `l` re-link, `x` placeholder
- plan/apply preview: `D` dry run, `A` apply

## Trust-Critical Behaviors

These behaviors should be enforced in code, not just described in the UI.

### 1. No unsafe implicit enablement

The plan builder must never auto-enable:

- bridges
- schedules
- send capabilities
- exec/system write actions
- unresolved credentials

### 2. Stable source references

Every displayed/imported item must retain a source reference string that survives into:

- `findings.kdl`
- `plan.kdl`
- generated files
- final report

### 3. Deterministic summaries

The counts shown in the TUI should come from the engine, not ad hoc UI counting, so the CLI and TUI stay consistent.

### 4. Reversible writes

Apply logic should create a migration directory and avoid scattering files until the operator promotes them.

## Markdown and KDL Outputs

The TUI should not be the only surface. Every major step should persist inspectable artifacts.

Recommended files:

- `migrate/findings.kdl`
- `migrate/report.md`
- `migrate/plan.kdl`
- `migrate/apply.log`

This matches the repo's CLI-first debugging philosophy.

## Tests

### Engine tests

- findings parsing and classification
- plan generation from fixed findings + decisions
- credential resolution rules
- portability classification stability
- deterministic KDL rendering

### TUI tests

- step transitions
- bulk-action behavior
- drawers opening and closing
- summaries derived from findings/plan
- blocked apply when unresolved high-risk issues remain

### Golden tests

- findings overview screen
- credentials screen
- plan preview screen
- results screen

## Detailed Test Design

The migration system should be tested as four layers:

- discovery engine
- planner engine
- apply engine
- TUI state/rendering

The goal is not just coverage. The goal is to prove that migration stays:

- deterministic,
- conservative,
- provenance-preserving,
- inspectable under failure.

### 1. Discovery engine tests

#### Config parsing matrix

Test cases:

- missing `openclaw.json` -> discovery succeeds with partial filesystem findings
- malformed `openclaw.json` -> report parse failure without crashing
- valid config with known top-level keys -> keys recognized and counted
- valid config with unknown top-level keys -> unknown keys preserved in findings
- nested channel/plugin/provider structures -> references discovered at full paths

Assertions:

- no panic on malformed input
- source path and parse status always written
- unknown keys are reported, not dropped

#### Artifact discovery matrix

Test cases:

- single `SKILL.md`
- nested skill trees
- slash command file heuristics
- standing-order markdown files
- plugin manifest files
- extension source files
- credential directories with session files
- mixed workspace garbage files that should not be classified

Assertions:

- artifact count matches fixtures
- kind classification is stable
- portability tier is assigned for each recognized artifact
- unrelated files do not create false positives

#### Credential detection matrix

Test cases by key pattern:

- `botToken`
- `appToken`
- `apiKey`
- `signingSecret`
- `webhookSecret`
- `clientId`
- `clientSecret`
- `authToken`
- `token`
- `cookie`
- `AWS_PROFILE`
- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`
- credential files like `credentials/whatsapp/.../creds.json`

Test cases by storage mode:

- inline string
- env-var reference `$FOO`
- env-var reference `${FOO}`
- credential file path
- empty value

Assertions:

- kind classification is correct
- storage classification is correct
- secret values are redacted in rendered output
- session-style credentials become `bridge_required`

#### Plugin classification matrix

Test cases:

- memory plugin names
- search plugin names
- vault plugin names
- browser plugin names
- chat/channel plugin names
- unknown plugin names

Assertions:

- nearest VIVARY category is correct
- portability tier matches design defaults
- reason string is populated and stable enough for operator review

#### Determinism tests

Test cases:

- same fixture scanned twice
- same fixture scanned with file walk ordering perturbed

Assertions:

- `findings.kdl` is byte-stable except for timestamp fields
- list ordering is deterministic

### 2. Planner engine tests

#### Decision application tests

Test cases:

- approve all safe artifacts
- defer all bridges
- placeholder unresolved credentials
- map one credential to vault ID
- mark one device session as re-auth required
- disable one otherwise importable artifact

Assertions:

- resulting `plan.kdl` reflects decisions exactly
- no unreviewed risky item is auto-enabled
- disabled/deferred items remain visible in the plan

#### Capability policy tests

Test cases:

- memory/search plugin findings -> propose `Memory_*` / `Search_*`
- channel findings -> do not propose in-core messaging enablement
- exec-related findings -> approval required by default
- write/send actions -> approval required by default
- read-only capabilities -> auto-allow only within proposed scope

Assertions:

- proposals match conservative defaults
- no capability appears without a reason/source reference
- changing operator decisions changes only targeted policy fields

#### Scope inference tests

Test cases:

- browser-related config with fixed URLs -> infer URL scope hints
- vault plugin with local path roots -> infer path-prefix hints
- memory/search plugin config with backend/source identifiers -> infer source-id hints
- ambiguous config -> preserve low-confidence hint instead of hard scope

Assertions:

- scope hints are preserved with confidence and reason
- planner uses high-confidence hints for proposed scopes only
- low-confidence hints are shown for review, not silently granted

#### Blocking logic tests

Test cases:

- unresolved setup-token
- unresolved device session
- unsupported native plugin preserved as report-only
- missing destination path
- conflicting destination writes

Assertions:

- blockers appear with `reason` and `next_action`
- blocker severity is correct
- plan generation still succeeds when blockers are non-fatal/reportable
- apply is prevented for fatal blockers

### 3. Apply engine tests

#### Dry-run tests

Test cases:

- dry-run plan with prompt imports only
- dry-run plan with bridges and blockers
- dry-run against pre-existing output directory

Assertions:

- no filesystem mutations occur beyond explicitly allowed report/preview outputs
- all planned operations are listed
- safety checks are surfaced

#### Write tests

Test cases:

- create prompts/macros/schedules/policies
- write `findings.kdl`, `report.md`, `plan.kdl`, `apply.log`
- create bridge stubs disabled by default
- preserve originals/report-only refs

Assertions:

- files land in expected locations
- generated files include provenance metadata
- disabled artifacts are marked disabled in contents where applicable

#### Overwrite and backup tests

Test cases:

- target file absent
- target file exists and is identical
- target file exists and differs
- backup directory already exists

Assertions:

- overwrite behavior follows explicit policy
- backups are created when required
- no silent destructive overwrite occurs

#### Partial failure tests

Test cases:

- failure writing one imported prompt
- failure writing bridge stub
- permission denied on output directory
- malformed plan during apply

Assertions:

- apply log records the failed operation
- already-written outputs remain inspectable
- user gets a clear partial-success / failure state
- no hidden rollback unless explicitly implemented and tested

### 4. TUI state machine tests

#### Step transition tests

Test cases:

- source select -> discovering -> findings overview
- findings overview -> exit
- findings overview -> review buckets -> credentials -> capabilities -> bridges -> plan preview
- back navigation between review steps
- apply preview -> applying -> results

Assertions:

- only allowed transitions succeed
- back navigation preserves decisions
- escape hatches work from allowed screens only

#### Dirty-state tests

Test cases:

- enter review step and change nothing
- approve/defer one item
- map one credential
- return to prior step

Assertions:

- dirty flag flips only after meaningful changes
- session state remains stable across step changes

#### Bulk action tests

Test cases:

- `Approve All Safe`
- `Map Known Env Vars`
- `Defer All Bridges`
- `Preserve All Unsupported As Report-Only`
- `Require Approval For All Writes`

Assertions:

- only intended items change
- blocked/risky items are not accidentally promoted
- summary counts refresh immediately

### 5. TUI rendering tests

#### Golden screen tests

Create golden outputs for:

- source select
- discovery scan
- findings overview
- review buckets
- credentials mapping
- capability review
- bridges/unsupported
- plan preview
- apply preview
- results

Golden fixtures should include:

- minimal install
- prompt-heavy install
- channel-heavy install
- memory/search-heavy install
- failure/blocked install

Assertions:

- trust-critical labels remain present
- dry-run badge remains visible where required
- unresolved counts and blockers are visible
- provenance hints and action labels remain stable

#### Drawer/modal tests

Test cases:

- provenance drawer open/close
- diff preview drawer open/close
- help drawer open/close
- error modal on failed discovery/apply

Assertions:

- drawers do not corrupt current selection
- close returns focus to prior context

### 6. CLI/TUI consistency tests

The CLI and TUI should agree on the underlying truth.

Test cases:

- generate findings via CLI and via TUI discovery
- generate plan via non-TUI planner and via TUI decisions

Assertions:

- counts match
- portability buckets match
- blockers match
- rendered output paths match

### 7. Fixture strategy

Maintain a set of reusable synthetic OpenClaw installs under test fixtures.

Recommended fixtures:

- `minimal/` - one config, one skill
- `skills_only/` - many prompt assets, no plugins
- `channels_heavy/` - Telegram/Slack/WhatsApp-like config and credential files
- `memory_search/` - memory/search plugins and provider refs
- `mixed_realistic/` - channels + skills + plugins + credentials + unsupported extensions
- `broken_install/` - malformed config, missing files, stale references

Each fixture should have expected:

- findings summary
- portability counts
- credential kinds
- plugin categories
- optional golden TUI screens

### 8. Regression tests for trust guarantees

These should be explicit named tests, not just implied by broader suites.

Required regression tests:

- no raw secret appears in `findings.kdl`
- no raw secret appears in `report.md`
- no unresolved credential becomes enabled during planning
- no bridge-required item becomes active by default
- no `Execution_*`, `Messaging_*`, or risky write capability is auto-enabled without review
- unsupported items remain visible in both plan and report
- every imported output includes source provenance
- approval-intent metadata never implies live enforcement when runtime support is absent

### 9. Performance and scale tests

Even though the migration is local, the UX should remain responsive.

Test cases:

- 1,000+ discovered artifacts
- 200+ credentials
- 100+ plugins/extensions
- large skill trees

Assertions:

- discovery completes within a reasonable test budget
- TUI summaries render without pathological slowdown
- scrolling/list filtering remains usable under large result sets

### 10. Recommended test execution stages

Use this progression for implementation:

1. engine unit tests for discovery classification
2. planner unit tests for conservative defaults and blockers
3. golden tests for findings overview and plan preview
4. apply tests with temp directories
5. regression tests for trust guarantees
6. large-fixture performance sanity tests

This ordering keeps the trust-sensitive core stable before UI polish work grows around it.

## Concrete Go Test Checklist

This section turns the test design into a proposed set of Go test files and test names.

The names below are intentionally explicit so implementation can proceed incrementally without inventing test structure on the fly.

### `internal/migrate/openclaw_discovery_test.go`

- `TestInspectOpenClaw_MissingConfigStillDiscoversFilesystemArtifacts`
- `TestInspectOpenClaw_MalformedConfigReportsParseFailure`
- `TestInspectOpenClaw_RecognizesKnownTopLevelKeys`
- `TestInspectOpenClaw_PreservesUnknownTopLevelKeys`
- `TestInspectOpenClaw_DiscoversNestedChannelAndPluginPaths`
- `TestInspectOpenClaw_DiscoversSkillAssets`
- `TestInspectOpenClaw_DiscoversStandingOrderFiles`
- `TestInspectOpenClaw_DiscoversSlashCommandFiles`
- `TestInspectOpenClaw_DiscoversExtensionCodeAsUnsupported`
- `TestInspectOpenClaw_DiscoversCredentialFiles`
- `TestInspectOpenClaw_IgnoresUnclassifiedNoiseFiles`
- `TestInspectOpenClaw_ProducesDeterministicArtifactOrdering`

### `internal/migrate/credential_classification_test.go`

- `TestClassifyCredentialKey_BotToken`
- `TestClassifyCredentialKey_AppToken`
- `TestClassifyCredentialKey_APIKey`
- `TestClassifyCredentialKey_SigningSecret`
- `TestClassifyCredentialKey_WebhookSecret`
- `TestClassifyCredentialKey_ClientID`
- `TestClassifyCredentialKey_ClientSecret`
- `TestClassifyCredentialKey_AuthToken`
- `TestClassifyCredentialKey_GenericToken`
- `TestClassifyCredentialKey_Cookie`
- `TestClassifyCredentialKey_AWSProfile`
- `TestClassifyCredentialKey_AWSAccessKeyPair`
- `TestClassifyCredentialKey_AWSBearerToken`
- `TestClassifyCredentialValue_Inline`
- `TestClassifyCredentialValue_EnvVarDollar`
- `TestClassifyCredentialValue_EnvVarBrace`
- `TestClassifyCredentialValue_CredentialFile`
- `TestClassifyCredentialValue_Empty`
- `TestCredentialPortability_DeviceSessionIsBridgeRequired`
- `TestCredentialPortability_CloudRefsRequireReview`
- `TestRenderKDL_RedactsInlineCredentialValues`

### `internal/migrate/plugin_classification_test.go`

- `TestClassifyPlugin_Memory`
- `TestClassifyPlugin_Search`
- `TestClassifyPlugin_Vault`
- `TestClassifyPlugin_Browser`
- `TestClassifyPlugin_Channel`
- `TestClassifyPlugin_UnknownDefaultsUnsupported`
- `TestInspectOpenClaw_IncrementsPluginCategoryCounts`

### `internal/migrate/findings_kdl_test.go`

- `TestWriteKDL_WritesDiscoveryHeader`
- `TestWriteKDL_WritesSummaryCounts`
- `TestWriteKDL_WritesArtifacts`
- `TestWriteKDL_WritesChannels`
- `TestWriteKDL_WritesPlugins`
- `TestWriteKDL_WritesCredentials`
- `TestWriteKDL_IsDeterministicExceptTimestamp`

### `internal/migrate/report_markdown_test.go`

Planned once `report.md` generation exists.

- `TestRenderReport_IncludesInstallOverview`
- `TestRenderReport_IncludesPortabilitySummary`
- `TestRenderReport_IncludesCredentialsSectionWithoutRawSecrets`
- `TestRenderReport_IncludesUnsupportedItems`
- `TestRenderReport_IncludesRecommendedNextActions`

### `internal/migrate/planner_test.go`

Planned once `plan.kdl` generation exists.

- `TestBuildPlan_ApproveAllSafeArtifacts`
- `TestBuildPlan_DeferAllBridgeItems`
- `TestBuildPlan_PlacesUnresolvedCredentialsAsBlockers`
- `TestBuildPlan_MapsVaultCredentialsIntoPlan`
- `TestBuildPlan_PreservesUnsupportedItemsAsReportOnly`
- `TestBuildPlan_ProposesCapabilitiesFromMemoryAndSearchFindings`
- `TestBuildPlan_UsesHighConfidenceScopeHintsInPolicyProposal`
- `TestBuildPlan_LowConfidenceScopeHintsRemainReviewOnly`
- `TestBuildPlan_DoesNotEnableMessagingForBridgeOnlyChannels`
- `TestBuildPlan_RequiresApprovalForExecAndWriteActions`
- `TestBuildPlan_AssignsProvenanceToEveryPlannedImport`
- `TestBuildPlan_IsDeterministicForSameInputs`

### `internal/migrate/scope_inference_test.go`

Planned once scope inference exists.

- `TestInferScopeHints_BrowserURLPrefixes`
- `TestInferScopeHints_VaultPathPrefixes`
- `TestInferScopeHints_MemoryBackendIdentifiers`
- `TestInferScopeHints_AmbiguousConfigProducesLowConfidenceHints`
- `TestInferScopeHints_PreservesReasonStrings`

### `internal/migrate/blockers_test.go`

Planned once blockers are first-class.

- `TestBuildPlan_BlocksUnresolvedSetupToken`
- `TestBuildPlan_BlocksDevicePairingSessionUntilRelinked`
- `TestBuildPlan_ReportsConflictingDestinationWrites`
- `TestBuildPlan_ReportsMissingOutputRoot`
- `TestBuildPlan_NonFatalUnsupportedItemsRemainVisible`

### `internal/migrate/apply_test.go`

Planned once apply exists.

- `TestApplyPlan_DryRunDoesNotWriteImportedFiles`
- `TestApplyPlan_DryRunProducesOperationSummary`
- `TestApplyPlan_WritesPromptMacroSchedulePolicyOutputs`
- `TestApplyPlan_WritesBridgeStubsDisabledByDefault`
- `TestApplyPlan_WritesApplyLog`
- `TestApplyPlan_CreatesBackupsBeforeOverwrite`
- `TestApplyPlan_LeavesIdenticalFilesUntouched`
- `TestApplyPlan_FailsClearlyOnPermissionDenied`
- `TestApplyPlan_RecordsPartialFailureWithoutHidingWrittenOutputs`

### `cmd/viv/migrate_test.go`

Some already exist; extend this file with CLI coverage.

- `TestRunMigrateOpenClawInspect_WritesFindingsFile`
- `TestRunMigrateOpenClawInspect_UsesDefaultOutputPath`
- `TestRunMigrateOpenClawInspect_RejectsUnknownTarget`
- `TestRunMigrateOpenClawInspect_RejectsUnknownSubcommand`
- `TestRunMigrateOpenClawInspect_ReportsSourceSummary`
- `TestRunMigrateOpenClawInspect_FailsOnMissingSourceDirectory`

### `cmd/viv/migrate_tui_state_test.go`

Planned once the TUI session model exists.

- `TestMigrateState_SourceSelectToDiscovering`
- `TestMigrateState_DiscoveringToFindingsOverview`
- `TestMigrateState_FindingsOverviewCanExit`
- `TestMigrateState_ReviewStepsPreserveDecisionsWhenGoingBack`
- `TestMigrateState_ApplyPreviewTransitionsToApplying`
- `TestMigrateState_ResultsCanExitCleanly`
- `TestMigrateState_DirtyFlagChangesOnlyAfterRealDecisions`

### `cmd/viv/migrate_tui_actions_test.go`

Planned once bulk actions exist.

- `TestMigrateAction_ApproveAllSafe`
- `TestMigrateAction_DeferAllBridges`
- `TestMigrateAction_MapKnownEnvVars`
- `TestMigrateAction_PreserveUnsupportedAsReportOnly`
- `TestMigrateAction_RequireApprovalForAllWrites`
- `TestMigrateAction_BulkActionsDoNotPromoteBlockedItems`

### `cmd/viv/migrate_tui_drawers_test.go`

Planned once drawers/modals exist.

- `TestMigrateDrawer_ProvenanceOpenClose`
- `TestMigrateDrawer_DiffPreviewOpenClose`
- `TestMigrateDrawer_HelpOpenClose`
- `TestMigrateDrawer_CloseRestoresPriorSelection`
- `TestMigrateModal_ErrorDoesNotLoseSessionState`

### `cmd/viv/migrate_tui_golden_test.go`

Planned once rendering exists.

- `TestMigrateView_SourceSelect_Golden`
- `TestMigrateView_DiscoveryScan_Golden`
- `TestMigrateView_FindingsOverview_Golden`
- `TestMigrateView_ReviewBuckets_Golden`
- `TestMigrateView_CredentialsReview_Golden`
- `TestMigrateView_CapabilityReview_Golden`
- `TestMigrateView_BridgeReview_Golden`
- `TestMigrateView_PlanPreview_Golden`
- `TestMigrateView_ApplyPreview_Golden`
- `TestMigrateView_Results_Golden`

### `cmd/viv/migrate_tui_consistency_test.go`

Planned once CLI and TUI plan generation share the same engine.

- `TestMigrateCLIAndTUI_ProduceSameFindingsSummary`
- `TestMigrateCLIAndTUI_ProduceSamePlanCounts`
- `TestMigrateCLIAndTUI_ProduceSameBlockerSet`

### `internal/migrate/trust_regression_test.go`

These should exist even if other suites already imply them.

- `TestTrust_NoRawSecretAppearsInFindingsKDL`
- `TestTrust_NoRawSecretAppearsInReportMarkdown`
- `TestTrust_UnresolvedCredentialCannotBecomeEnabled`
- `TestTrust_BridgeRequiredItemsRemainDisabledByDefault`
- `TestTrust_RiskyCapabilitiesRequireExplicitReview`
- `TestTrust_UnsupportedItemsRemainVisibleInPlanAndReport`
- `TestTrust_AllImportedOutputsCarryProvenance`
- `TestTrust_ApprovalIntentDoesNotMasqueradeAsRuntimeEnforcement`

### `internal/migrate/performance_test.go`

Prefer these as non-flaky sanity tests, not microbenchmarks.

- `TestInspectOpenClaw_LargeFixtureCompletesWithinBudget`
- `TestBuildPlan_LargeFixtureCompletesWithinBudget`
- `BenchmarkInspectOpenClaw_MixedRealisticFixture`
- `BenchmarkBuildPlan_MixedRealisticFixture`

## Suggested Fixture Files and Helpers

Recommended fixture roots:

- `internal/migrate/testdata/minimal/`
- `internal/migrate/testdata/skills_only/`
- `internal/migrate/testdata/channels_heavy/`
- `internal/migrate/testdata/memory_search/`
- `internal/migrate/testdata/mixed_realistic/`
- `internal/migrate/testdata/broken_install/`

Recommended helper files:

- `internal/migrate/testutil_test.go`
- `cmd/viv/migrate_testutil_test.go`

Recommended helpers:

- `mustLoadFixtureTree(t, name)`
- `mustInspectFixture(t, name)`
- `mustBuildPlanFixture(t, name, decisions)`
- `mustReadGolden(t, name)`
- `assertNoSecretLeak(t, text)`
- `assertHasProvenance(t, pathOrText)`

## Suggested Implementation Order For Tests

If implemented incrementally, build the suite in this order:

1. `internal/migrate/openclaw_discovery_test.go`
2. `internal/migrate/credential_classification_test.go`
3. `internal/migrate/plugin_classification_test.go`
4. `internal/migrate/findings_kdl_test.go`
5. extend `cmd/viv/migrate_test.go`
6. `internal/migrate/trust_regression_test.go`
7. planner/apply tests once those engines exist
8. TUI state tests
9. TUI golden tests

This order keeps the semantic core stable before the UI surface expands.

## Recommended First Build Order

1. add `report.md` generation to v0
2. add a read-only migration TUI shell around current `findings.kdl`
3. implement findings overview and list/detail browsing
4. add provenance drawer
5. add operator decision state and write `plan.kdl`
6. add credentials screen
7. add capability review screen
8. add plan preview and dry-run apply preview
9. add live apply

## Recommendation

Do not jump straight from `findings.kdl` to live import. Build the migration TUI as a deterministic planner with a strong read-only phase first. The operator should trust the system because:

- discovery is visible,
- decisions are explicit,
- outputs are inspectable,
- and the apply step is the final, narrow step rather than the main event.
