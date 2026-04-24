## MODIFIED Requirements

### Requirement: Section kinds
Each section in a descriptor SHALL have a `kind` field from a closed set: `form`, `iframe`, `status`, `logs`, `action`. Unknown kinds SHALL be rejected at registration.

#### Scenario: Form section
- **WHEN** a section declares `kind: "form"`
- **THEN** the section SHALL include `title` (string), `config_key` (string, the section name for `PUT /v1/config`), and `fields` (object: field name → field schema)

#### Scenario: Iframe section
- **WHEN** a section declares `kind: "iframe"`
- **THEN** the section SHALL include `title` (string), `url` (string, the URL to embed), and optional `height` (string, CSS height value, default `"600px"`)

#### Scenario: Status section
- **WHEN** a section declares `kind: "status"`
- **THEN** the section SHALL include `title` (string), `url` (string, a URL the dashboard GETs for data), and `layout` (enum: `"key_value"` or `"table"`)

#### Scenario: Logs section
- **WHEN** a section declares `kind: "logs"`
- **THEN** the section SHALL include `title` (string), `tail_url` (string), `history_url` (string), `services_url` (string), optional `default_services` (array), and `default_level` (enum: `"debug"`, `"info"`, `"warn"`, `"error"`; default `"info"`)

#### Scenario: Action section
- **WHEN** a section declares `kind: "action"`
- **THEN** the section SHALL include `title` (string) and `actions` (non-empty array of action descriptors)

#### Scenario: Unknown kind rejected
- **WHEN** a section's `kind` is not one of `form`, `iframe`, `status`, `logs`, `action`
- **THEN** the gateway SHALL reject the entire descriptor and log `dashboard_panel_invalid` with the unknown kind

## ADDED Requirements

### Requirement: Action descriptor fields
Each action in an `action` section SHALL include: `label` (string, the button text), `url` (string, the path to POST to), `method` (enum: `"POST"`, `"PUT"`, `"DELETE"`; default `"POST"`), and MAY include: `body` (object, static body sent with the request), `params` (array of parameter definitions that the dashboard renders as inputs alongside the button), `confirm` (string, confirmation prompt message; button requires acknowledgment before firing), and `status_key` (string, JSON path in the response used to render success text).

#### Scenario: Minimum valid action
- **WHEN** an action declares a `label` and `url`
- **THEN** the descriptor is valid; the dashboard SHALL render a button that POSTs an empty body to the URL when clicked

#### Scenario: Parameterized action
- **WHEN** an action declares `params: [{name: "session_id", type: "string", required: true}]`
- **THEN** the dashboard SHALL render an input for `session_id` adjacent to the button, validate it is non-empty before enabling the button, and include it in the POST body

#### Scenario: Confirmed action
- **WHEN** an action declares `confirm: "This will restart the service. Continue?"`
- **THEN** clicking the button SHALL show a confirmation dialog with that message; the request SHALL fire only on confirm

#### Scenario: Status key rendering
- **WHEN** an action declares `status_key: "operation_id"` and the response includes that field
- **THEN** the dashboard SHALL display the field's value in the action's status area

#### Scenario: Method override
- **WHEN** an action declares `method: "DELETE"`
- **THEN** the dashboard SHALL issue a DELETE request rather than POST
