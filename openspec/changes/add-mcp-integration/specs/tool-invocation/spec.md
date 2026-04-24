## ADDED Requirements

### Requirement: Registry accepts MCP-sourced tools
The `tools.Registry.Register` method SHALL accept MCP-sourced tool implementations using the same interface as built-in tools. The namespacing rule (names starting with `mcp__`) distinguishes MCP tools from built-ins, and the rule SHALL be enforced by rejecting built-in registrations whose name starts with `mcp__`.

#### Scenario: MCP tool registered
- **WHEN** an MCP-sourced tool with `Name() == "mcp__foo__bar"` is registered via `Registry.Register`
- **THEN** the registration SHALL succeed and the tool SHALL be invocable via `Registry.Invoke(ctx, "mcp__foo__bar", args)`

#### Scenario: Built-in with mcp__ prefix rejected
- **WHEN** a non-MCP tool (not sourced via the MCP manager) is registered with a name starting with `mcp__`
- **THEN** `Registry.Register` SHALL return a non-nil error, preserving the invariant that only MCP-managed tools occupy the `mcp__` namespace

### Requirement: MCP tool registration is additive to built-ins
Main-agent SHALL register built-in tools (`list_skills`, `read_skill`) before MCP-sourced tools, so that `Registry.Schemas()` order places built-ins first. This preserves the slice-2 behavior where the LLM's `tools` array always begins with the built-ins.

#### Scenario: Registration order
- **WHEN** main-agent initializes with built-ins and one or more MCP servers
- **THEN** the order of tools in `Registry.Schemas()` SHALL be: `list_skills`, `read_skill`, then MCP tools in the order they were registered (which is the order returned by each server's `tools/list`, across servers in config order)

#### Scenario: No MCP servers configured
- **WHEN** `config:mcp:servers` is empty or missing
- **THEN** main-agent SHALL still register built-ins and `Registry.Schemas()` SHALL return exactly those built-ins, byte-identical to slice-2 behavior
