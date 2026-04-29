## MODIFIED Requirements

### Requirement: Dashboard renders descriptor-driven panels
The dashboard JS SHALL render each panel from its descriptor by interpreting the `sections` array. Section rendering follows the rules in the `dashboard-panel-registry` capability (form / iframe / status).

#### Scenario: Form section rendered
- **WHEN** a panel contains a section with `kind: "form"` and a `fields` object
- **THEN** the dashboard SHALL render a form with one input per field according to the field's `type` and constraints, a save button, and MAY include a status indicator for save success/failure

#### Scenario: Form save round-trip
- **WHEN** the user modifies form values and clicks save
- **THEN** the dashboard SHALL PUT `/v1/config` with `{"section": <config_key>, "values": <form_data>}`, display success on HTTP 200, and display the error message on non-2xx

#### Scenario: Iframe section rendered with non-localhost URL
- **WHEN** a panel contains a section with `kind: "iframe"` and a `url` whose hostname is NOT `localhost` or `127.0.0.1`
- **THEN** the dashboard SHALL render an `<iframe src="<url>" sandbox="allow-scripts allow-same-origin allow-forms">` at the declared `height` (or 600px default), using the URL as-is

#### Scenario: Iframe section rendered with localhost URL
- **WHEN** a panel contains a section with `kind: "iframe"` and a `url` whose hostname is `localhost` or `127.0.0.1`
- **THEN** the dashboard SHALL replace the URL's hostname with `window.location.hostname`, preserving the original port, path, and query parameters, and render an `<iframe>` with the resolved URL

#### Scenario: Status section rendered
- **WHEN** a panel contains a section with `kind: "status"`, a `url`, and a `layout`
- **THEN** the dashboard SHALL GET the URL and render the response as key-value pairs (if `layout: "key_value"`) or as a table (if `layout: "table"`)
