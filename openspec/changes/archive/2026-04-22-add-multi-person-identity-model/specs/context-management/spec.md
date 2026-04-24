## ADDED Requirements

### Requirement: ContextAssembledPayload carries speaker_id
The `ContextAssembledPayload` published by the context manager SHALL include a `speaker_id` field that echoes the value resolved by the gateway for the originating turn. The field SHALL be present on every payload; when the gateway resolved the speaker to `"unknown"`, the field SHALL be the literal string `"unknown"`.

#### Scenario: Known speaker propagated
- **WHEN** the gateway publishes a request with resolved `speaker_id="jason"`
- **THEN** the `ContextAssembledPayload` emitted by the context manager SHALL have `speaker_id="jason"`

#### Scenario: Unknown speaker propagated
- **WHEN** the gateway resolves a request's speaker to `"unknown"`
- **THEN** the `ContextAssembledPayload` SHALL have `speaker_id="unknown"`

### Requirement: Memory recall is scoped by speaker when configured
During context assembly, the context manager SHALL call memory-service `/recall` with `speaker_id` set to the turn's resolved speaker when `config:memory.recall_default_speaker_scope` is one of `primary` or `explicit`. When the scope is `any`, the context manager SHALL NOT add an implicit `speaker_id` filter to the recall call.

#### Scenario: Scope `primary` scopes recall
- **WHEN** `config:memory.recall_default_speaker_scope="primary"` and the turn's resolved speaker is `"jason"`
- **THEN** the context manager's `/recall` call SHALL include `speaker_id="jason"`

#### Scenario: Scope `any` does not scope recall
- **WHEN** `config:memory.recall_default_speaker_scope="any"`
- **THEN** the context manager's `/recall` call SHALL NOT include a `speaker_id` field

#### Scenario: Scope `explicit` without speaker_id logs warning
- **WHEN** `config:memory.recall_default_speaker_scope="explicit"` and the turn's resolved speaker is `"unknown"`
- **THEN** the context manager SHALL skip the recall call, log WARN with `msg="context_recall_skipped_no_speaker"`, and proceed with context assembly without memory injection
