## ADDED Requirements

### Requirement: Panel descriptor schema
The system SHALL define a panel descriptor type that services use to declare their dashboard panel presence. A descriptor SHALL have: `version` (integer, currently 1), `title` (string), optional `order` (integer, default derived from service ID), and `sections` (array of section descriptors with at least one entry).

#### Scenario: Minimum valid descriptor
- **WHEN** a service registers with a descriptor containing `version: 1`, a non-empty `title`, and at least one valid section
- **THEN** the gateway SHALL accept the descriptor and expose it via `GET /v1/dashboard/panels`

#### Scenario: Missing required field rejected
- **WHEN** a service registers with a descriptor that omits `title` or `sections` or provides `sections: []`
- **THEN** the gateway SHALL reject the descriptor, log at WARN with `msg: "dashboard_panel_invalid"` and the offending service ID, and SHALL NOT include it in `GET /v1/dashboard/panels`

#### Scenario: Unknown version tolerated
- **WHEN** a service registers with a descriptor whose `version` is higher than the gateway knows about
- **THEN** the gateway SHALL log at INFO and skip the descriptor (forward compatibility: newer services tolerated by older gateways)

### Requirement: Section kinds
Each section in a descriptor SHALL have a `kind` field from a closed set: `form`, `iframe`, `status`. Unknown kinds SHALL be rejected at registration.

#### Scenario: Form section
- **WHEN** a section declares `kind: "form"`
- **THEN** the section SHALL include `title` (string), `config_key` (string, the section name for `PUT /v1/config`), and `fields` (object: field name → field schema)

#### Scenario: Iframe section
- **WHEN** a section declares `kind: "iframe"`
- **THEN** the section SHALL include `title` (string), `url` (string, the URL to embed), and optional `height` (string, CSS height value, default `"600px"`)

#### Scenario: Status section
- **WHEN** a section declares `kind: "status"`
- **THEN** the section SHALL include `title` (string), `url` (string, a URL the dashboard GETs for data), and `layout` (enum: `"key_value"` or `"table"`)

#### Scenario: Unknown kind rejected
- **WHEN** a section's `kind` is not one of `form`, `iframe`, `status`
- **THEN** the gateway SHALL reject the entire descriptor and log `dashboard_panel_invalid` with the unknown kind

### Requirement: Form field schema dialect
Form sections use a small field schema dialect. Each field SHALL declare `type` from the set: `string`, `number`, `integer`, `boolean`, `enum`, `textarea`. Each field MAY include `label`, `description`, `default`, `readonly` (boolean), and type-specific constraints (`min`, `max`, `step`, `values`).

#### Scenario: String field
- **WHEN** a field declares `type: "string"`
- **THEN** the dashboard SHALL render a single-line text input with the declared `label`

#### Scenario: Textarea field
- **WHEN** a field declares `type: "textarea"`
- **THEN** the dashboard SHALL render a multi-line text area, sized to be useful for multi-paragraph content (min 6 rows)

#### Scenario: Enum field
- **WHEN** a field declares `type: "enum"` with `values: ["a","b","c"]`
- **THEN** the dashboard SHALL render a dropdown/select with exactly those three options

#### Scenario: Number/integer constraints
- **WHEN** a field declares `type: "number"` or `type: "integer"` with any of `min`, `max`, `step`
- **THEN** the dashboard SHALL apply those constraints to its number input

#### Scenario: Readonly field
- **WHEN** a field declares `readonly: true`
- **THEN** the dashboard SHALL display its current value but SHALL NOT allow editing and SHALL NOT submit its value on save

#### Scenario: Unknown field type rejected
- **WHEN** a field declares a `type` not in the allowed set
- **THEN** the gateway SHALL reject the descriptor

### Requirement: Gateway aggregation endpoint
The gateway SHALL expose `GET /v1/dashboard/panels` that returns the currently-registered panel descriptors, including its own built-in descriptors, as a JSON object `{"panels": [<descriptor>, ...]}`.

#### Scenario: Ordering
- **WHEN** the endpoint aggregates descriptors for response
- **THEN** descriptors with explicit `order` values SHALL sort first by `order` ascending, then remaining descriptors SHALL sort by service ID ascending

#### Scenario: Built-in panels included
- **WHEN** the gateway starts
- **THEN** it SHALL synthesize descriptors for its built-in panels (chat, sessions, system) and include them in the aggregation alongside service-contributed panels

#### Scenario: Dead services excluded
- **WHEN** a service has deregistered or failed its heartbeat
- **THEN** its panel descriptor SHALL be excluded from the aggregation

#### Scenario: No descriptors yet
- **WHEN** no services have registered a panel and no built-ins are defined
- **THEN** the endpoint SHALL return `{"panels": []}` with HTTP 200, not an error

### Requirement: Reserved order range
Orders in the range `[0, 99]` SHALL be reserved for gateway built-in panels. Services contributing a descriptor with `order < 100` SHALL have their order clamped to 100.

#### Scenario: Service order clamped
- **WHEN** a service registers with `order: 5`
- **THEN** the gateway SHALL use `order: 100` for sorting and SHALL log at WARN with `msg: "dashboard_panel_order_clamped"` and the service ID

### Requirement: Iframe sandbox attributes
The dashboard SHALL render `iframe` sections with the HTML `sandbox` attribute set to a minimal permission list: `allow-scripts allow-same-origin allow-forms`. This permits the embedded app to function (e.g. Hindsight's Control Plane uses JS and forms) while preventing navigation hijack.

#### Scenario: Sandbox attributes applied
- **WHEN** the dashboard renders an iframe section
- **THEN** the resulting `<iframe>` element SHALL have `sandbox="allow-scripts allow-same-origin allow-forms"`
