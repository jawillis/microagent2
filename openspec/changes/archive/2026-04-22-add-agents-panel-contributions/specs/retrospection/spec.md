## ADDED Requirements

### Requirement: retro-agent registers with panel descriptor
retro-agent SHALL include a `dashboard_panel` descriptor in its existing registration payload. The descriptor SHALL have two sections: a form for retro policy (existing config_key `retro`) and an action section for manual job triggers.

#### Scenario: Panel descriptor sections
- **WHEN** retro-agent constructs its descriptor
- **THEN** the descriptor SHALL have `title: "Retro"`, `order: 320`, and sections:
  - form section with `config_key: "retro"` and fields:
    - `inactivity_timeout_s` (integer, min 10, default 300)
    - `skill_dup_threshold` (number, min 0, max 1, step 0.01, default 0.85)
    - `min_history_turns` (integer, min 1, default 4)
    - `curation_categories` (string, comma-separated, default `"identity,preferences,technical,home,ephemera"`)
    - `curation_recall_limit` (integer, min 1, default 15)
    - `mental_model_refresh_s` (integer, min 60, default 3600, description: "Cadence for Mental Model refresh; takes effect once mental-model support lands")
  - action section with three actions for manual triggers (see below)

#### Scenario: Action parameters for session ID
- **WHEN** the retro panel renders action buttons
- **THEN** each action SHALL include `params: [{name: "session_id", type: "string", required: true}]` so operators enter a session ID before triggering

### Requirement: Manual retro trigger actions
The retro panel's action section SHALL declare three actions targeting the existing `POST /v1/retro/{session}/trigger` endpoint with varying `job_type` values: `memory_extraction`, `skill_creation`, and `curation`.

#### Scenario: Memory extraction trigger
- **WHEN** the operator enters a session ID and clicks "Run Memory Extraction"
- **THEN** the dashboard SHALL POST `/v1/retro/{session_id}/trigger` with body `{"job_type": "memory_extraction"}`, display a success status on HTTP 200, and display the error on non-2xx

#### Scenario: Skill creation trigger
- **WHEN** the operator clicks "Run Skill Creation"
- **THEN** same as memory_extraction but with `job_type: "skill_creation"`

#### Scenario: Curation trigger
- **WHEN** the operator clicks "Run Curation"
- **THEN** same as above with `job_type: "curation"`

#### Scenario: Missing session ID blocks action
- **WHEN** the operator clicks a trigger button without entering a session ID
- **THEN** the dashboard SHALL disable the button until a non-empty session ID is entered (per the action's `params.required: true`)

### Requirement: Retro config extended with curation_recall_limit and mental_model_refresh_s
`RetroConfig` SHALL include `CurationRecallLimit` (int) and `MentalModelRefreshS` (int) fields. Defaults: 15 and 3600 respectively.

#### Scenario: Defaults applied
- **WHEN** `ResolveRetro` reads config and the new keys are absent
- **THEN** it SHALL apply the defaults

#### Scenario: Dashboard edits persist
- **WHEN** the operator saves the retro form with a new `curation_recall_limit`
- **THEN** `config:retro.curation_recall_limit` SHALL reflect the new value and subsequent retro-agent `ResolveRetro` reads SHALL see it
