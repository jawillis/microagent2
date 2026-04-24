## ADDED Requirements

### Requirement: Built-in read_skill_file tool
A built-in tool named `read_skill_file` SHALL be registered at main-agent startup. Its schema SHALL describe a function that takes two required string parameters:
- `skill`: the exact skill name as returned by `list_skills`
- `path`: a relative path within the skill directory (e.g. `reference/best_practices.md`)

Its `Invoke` SHALL call `skills.Store.ReadFile(skill, path)` and translate the three-valued result into the existing tool-result string convention:

| Store result | Tool result |
|---|---|
| `(contents, true, nil)` | `contents` (the raw bytes as-is) |
| `("", false, nil)` | `{"error":"skill not found: <skill>"}` |
| `("", true, err)` | `{"error":"<err.Error()>"}` |

The tool SHALL also return structured errors for argument-parsing failures, consistent with the existing `read_skill` tool.

#### Scenario: Successful read returns file contents
- **WHEN** `read_skill_file` is invoked with `{"skill":"code-review","path":"language-notes.md"}` and the file exists and is within the cap
- **THEN** the result SHALL be the exact file contents, byte-for-byte

#### Scenario: Unknown skill returns structured error
- **WHEN** `read_skill_file` is invoked with `{"skill":"nonexistent","path":"x.md"}`
- **THEN** the result SHALL be `{"error":"skill not found: nonexistent"}`

#### Scenario: Path rejection surfaces store error message
- **WHEN** `read_skill_file` is invoked with `{"skill":"code-review","path":"../../etc/passwd"}`
- **THEN** the result SHALL be a JSON error object whose `error` field contains the store's rejection message (e.g. mentioning "outside the skill root" or "escapes")
- **AND** NO filesystem access SHALL occur outside the skill root

#### Scenario: Missing required argument
- **WHEN** `read_skill_file` is invoked with `{}` or with only one of the two required fields set to a non-empty string
- **THEN** the result SHALL be `{"error":"skill and path arguments are required"}` (or equivalent messaging naming the missing field)

#### Scenario: Reserved path SKILL.md redirected
- **WHEN** `read_skill_file` is invoked with `{"skill":"code-review","path":"SKILL.md"}`
- **THEN** the result SHALL be a JSON error object whose `error` field directs the caller to use `read_skill`
- **AND** the file SHALL NOT be served by this tool even though it exists

#### Scenario: Oversize file returns error
- **WHEN** `read_skill_file` is invoked with a valid path pointing to a file larger than `SKILL_FILE_MAX_BYTES`
- **THEN** the result SHALL be a JSON error object reporting the size and the cap

#### Scenario: Malformed JSON arguments
- **WHEN** `read_skill_file` is invoked with an `argsJSON` string that does not parse as a JSON object
- **THEN** the result SHALL be `{"error":"invalid arguments: <detail>"}`

## MODIFIED Requirements

### Requirement: MCP tool registration is additive to built-ins
Main-agent SHALL register built-in tools (`list_skills`, `read_skill`, `read_skill_file`) before MCP-sourced tools, so that `Registry.Schemas()` order places built-ins first. This preserves the slice-2 invariant where the LLM's `tools` array always begins with the built-ins.

#### Scenario: Registration order
- **WHEN** main-agent initializes with built-ins and one or more MCP servers
- **THEN** the order of tools in `Registry.Schemas()` SHALL be: `list_skills`, `read_skill`, `read_skill_file`, then MCP tools in the order they were registered (which is the order returned by each server's `tools/list`, across servers in config order)

#### Scenario: No MCP servers configured
- **WHEN** `config:mcp:servers` is empty or missing
- **THEN** main-agent SHALL still register all three built-ins and `Registry.Schemas()` SHALL return exactly those built-ins in order: `list_skills`, `read_skill`, `read_skill_file`

#### Scenario: Built-in registered before MCP
- **WHEN** main-agent starts with one MCP server that exposes tool `foo` and registers its built-ins first
- **THEN** `Registry.Schemas()` SHALL return `list_skills`, `read_skill`, `read_skill_file`, `mcp__<server>__foo` in that exact order
