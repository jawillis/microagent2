## MODIFIED Requirements

### Requirement: Section kinds
Each section in a descriptor SHALL have a `kind` field from a closed set: `form`, `iframe`, `status`, `logs`. Unknown kinds SHALL be rejected at registration.

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
- **THEN** the section SHALL include `title` (string), `tail_url` (string, SSE endpoint path), `history_url` (string, history fetch endpoint path), `services_url` (string, path to a services-list endpoint), `default_services` (optional array of service IDs; if absent the dashboard treats "all discovered services" as the default), and `default_level` (enum: `"debug"`, `"info"`, `"warn"`, `"error"`; default `"info"`)

#### Scenario: Unknown kind rejected
- **WHEN** a section's `kind` is not one of `form`, `iframe`, `status`, `logs`
- **THEN** the gateway SHALL reject the entire descriptor and log `dashboard_panel_invalid` with the unknown kind
