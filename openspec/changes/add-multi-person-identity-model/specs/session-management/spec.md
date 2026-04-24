## ADDED Requirements

### Requirement: Session stores last-known speaker_id
Every session record in Valkey SHALL include an optional `last_speaker_id` field. The field SHALL be set to the resolved `speaker_id` of the most recent turn associated with the session. The field SHALL be absent on sessions created before this change (no backfill).

#### Scenario: First turn sets last_speaker_id
- **WHEN** a new session is created and its first turn resolves to `speaker_id="jason"`
- **THEN** the session record SHALL have `last_speaker_id="jason"`

#### Scenario: Subsequent turn updates last_speaker_id
- **WHEN** an existing session's next turn resolves to `speaker_id="alice"`
- **THEN** the session record SHALL be updated so `last_speaker_id="alice"`

#### Scenario: Unknown speaker stored verbatim
- **WHEN** a turn resolves to `speaker_id="unknown"`
- **THEN** the session record SHALL store `last_speaker_id="unknown"` (the field SHALL NOT be omitted)

### Requirement: List sessions includes last_speaker_id
The `GET /v1/sessions` endpoint SHALL include `last_speaker_id` in each session object when the field is present on the record.

#### Scenario: Mixed pre-change and post-change sessions
- **WHEN** a client calls `GET /v1/sessions` and some sessions were created before this change while others were created after
- **THEN** the response array SHALL include `last_speaker_id` on post-change sessions and SHALL omit it on pre-change sessions

### Requirement: View session history includes per-turn speaker_id
The `GET /v1/sessions/:id` endpoint SHALL include a `speaker_id` field on each turn in the reconstructed conversation when the turn was persisted with a resolved speaker.

#### Scenario: Turns include speaker_id
- **WHEN** a client calls `GET /v1/sessions/:id` for a session with multiple turns each resolved to different speakers
- **THEN** each turn object SHALL include its own `speaker_id`

#### Scenario: Legacy turn without speaker_id
- **WHEN** a turn was persisted before this change (no `speaker_id` stored)
- **THEN** its turn object in the session-history response SHALL omit the `speaker_id` field (no synthetic value)

## MODIFIED Requirements

### Requirement: Hybrid session ID strategy
The gateway SHALL associate sessions with response chains. When a Responses API request has no `previous_response_id`, the gateway SHALL create a new session and associate it with the response. When a request has a `previous_response_id`, the gateway SHALL inherit the session ID from the previous response. For chat/completions requests, the gateway SHALL accept an optional `session_id` in the request body (if provided, associate the response with that session; if omitted, create a new session). For both endpoints, the gateway SHALL additionally accept an optional `speaker_id` field (body) or `X-Speaker-ID` header that is associated with the specific turn and stored on the session record per the identity-model spec.

#### Scenario: New response chain creates session
- **WHEN** a Responses API request has no `previous_response_id`
- **THEN** the gateway SHALL create a new session with a generated UUIDv4 and associate the response with it

#### Scenario: Continued chain inherits session
- **WHEN** a Responses API request includes a valid `previous_response_id`
- **THEN** the gateway SHALL read the `session_id` from the previous response and assign the same session to the new response

#### Scenario: Chat/completions with client-provided session_id
- **WHEN** a chat/completions request includes a `session_id` field
- **THEN** the gateway SHALL associate the response with the provided session ID

#### Scenario: Chat/completions without session_id
- **WHEN** a chat/completions request omits the `session_id` field
- **THEN** the gateway SHALL create a new session and associate the response with it

#### Scenario: Speaker_id on body
- **WHEN** a chat/completions or Responses request includes `speaker_id="alice"` in the body
- **THEN** the gateway SHALL resolve the turn's speaker to `"alice"` and store it on the turn and as `last_speaker_id` on the session

#### Scenario: Speaker_id on header
- **WHEN** a request includes `X-Speaker-ID: bob` header and no body `speaker_id`
- **THEN** the gateway SHALL resolve the turn's speaker to `"bob"`
