## ADDED Requirements

### Requirement: Chat transcript renders tool-call events as collapsed status blocks
The dashboard's Chat panel transcript SHALL render each tool-call event received via the streaming `response.tool_call` SSE event kind as a collapsed status block, visually distinct from user and assistant text turns, showing the tool name and a disclosure affordance to expand and inspect the arguments. Tool-result messages (assistant turns referencing `tool_call_id` results) SHALL render in the same collapsed style.

#### Scenario: Tool call rendered as collapsed block
- **WHEN** the transcript receives a `response.tool_call` SSE event for tool `list_skills` with arguments `{}`
- **THEN** the transcript SHALL append a collapsed status block with a label identifying the tool name (for example `🔧 list_skills`), an expand/collapse affordance, and SHALL NOT append any text content to the surrounding assistant turn for that event

#### Scenario: Expanded block shows full arguments
- **WHEN** the user clicks or activates the disclosure affordance on a tool-call status block
- **THEN** the block SHALL expand to show the full `function.arguments` JSON, formatted for readability

#### Scenario: Tool-call blocks do not pollute text turns
- **WHEN** an assistant turn includes both streamed text tokens and a tool-call event
- **THEN** the rendered assistant turn SHALL show the text content in the normal transcript style, and the tool-call block SHALL render as a separate collapsed element adjacent to the assistant turn, with no tool-call JSON appearing inside the text content

### Requirement: Tool-call blocks are keyboard-accessible
The collapsed tool-call status blocks SHALL be keyboard-operable with native disclosure semantics, so that expand/collapse works without pointer input.

#### Scenario: Keyboard activation
- **WHEN** the user focuses a collapsed tool-call block and presses Enter or Space
- **THEN** the block SHALL toggle between collapsed and expanded states
