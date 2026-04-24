## ADDED Requirements

### Requirement: Filesystem-backed skill catalog
The skills store SHALL scan a configurable root directory (`SKILLS_DIR`, default `./skills/`) at process startup, identify each subdirectory that contains a `SKILL.md` file as a single skill, parse the file's YAML frontmatter and Markdown body, and expose the resulting catalog as an in-memory collection indexed by skill name.

#### Scenario: Default skills root
- **WHEN** the skills store initializes and no `SKILLS_DIR` environment variable is set
- **THEN** it SHALL scan `./skills/` relative to the process's working directory

#### Scenario: Configurable root
- **WHEN** `SKILLS_DIR` is set to a non-empty path
- **THEN** the skills store SHALL scan that path instead of the default

#### Scenario: Root missing is non-fatal
- **WHEN** the configured root directory does not exist or is not readable
- **THEN** the skills store SHALL initialize with an empty catalog, log at WARN with `msg: "skills_dir_unreadable"` and fields `{path, error}`, and SHALL NOT fail the process

#### Scenario: One-level scan
- **WHEN** scanning the root
- **THEN** the store SHALL look for `SKILL.md` in each immediate subdirectory of the root; nested skills (e.g. `root/outer/inner/SKILL.md`) SHALL be ignored

### Requirement: SKILL.md frontmatter parsing
Each `SKILL.md` file SHALL begin with a YAML frontmatter block delimited by lines containing only `---`. The frontmatter SHALL contain `name` and `description` fields (both strings). It MAY contain `allowed-tools` (array of strings) and `model` (string). Everything after the closing `---` SHALL be treated as the skill's Markdown body.

#### Scenario: Minimal valid skill
- **WHEN** a `SKILL.md` contains a frontmatter block with `name: estimate-tokens` and `description: Estimate token count`, followed by Markdown text
- **THEN** the store SHALL register the skill with `Name == "estimate-tokens"`, `Description == "Estimate token count"`, and `Body` containing the Markdown portion (leading whitespace after `---` trimmed)

#### Scenario: Missing required field
- **WHEN** a `SKILL.md` lacks `name` or `description` in its frontmatter, or has an empty value for either
- **THEN** the store SHALL log at WARN with `msg: "skill_manifest_invalid"` and fields `{path, reason}`, and SHALL NOT add the skill to the catalog

#### Scenario: Malformed frontmatter
- **WHEN** a `SKILL.md` does not begin with `---` or its frontmatter is not valid YAML
- **THEN** the store SHALL log at WARN with `msg: "skill_frontmatter_parse_failed"` and fields `{path, error}`, and SHALL skip that directory

#### Scenario: Optional fields preserved
- **WHEN** a `SKILL.md` frontmatter includes `allowed-tools: [list_skills, read_skill]` and `model: gpt-4`
- **THEN** the store SHALL parse and retain those values on the skill's `Manifest`, even though this slice does not act on them

### Requirement: Catalog API
The skills store SHALL expose a `List()` method returning all parsed manifests in deterministic order (alphabetical by name), a `Get(name)` method returning the manifest and a presence flag, and a `Body(name)` method returning the Markdown body and a presence flag.

#### Scenario: Deterministic ordering
- **WHEN** `List()` is invoked on a store populated with skills `b-skill`, `a-skill`, `c-skill`
- **THEN** it SHALL return manifests in the order `a-skill`, `b-skill`, `c-skill`

#### Scenario: Get hit
- **WHEN** `Get("a-skill")` is invoked and the skill exists
- **THEN** the call SHALL return `(*Manifest, true)` where the `Manifest.Name == "a-skill"`

#### Scenario: Get miss
- **WHEN** `Get("nonexistent")` is invoked and no such skill is registered
- **THEN** the call SHALL return `(nil, false)`

#### Scenario: Body returns the Markdown
- **WHEN** `Body("a-skill")` is invoked and the skill exists with body `"Step 1. Do X."`
- **THEN** the call SHALL return `("Step 1. Do X.", true)` (exact body bytes, trimmed of the frontmatter block and the leading blank line following it)

### Requirement: Scan is one-shot at startup
The skills store SHALL NOT re-scan the filesystem after initialization. Operators SHALL restart the main-agent to pick up added, modified, or removed skills.

#### Scenario: File changes invisible to running store
- **WHEN** a skill's `SKILL.md` is modified while the main-agent is running
- **THEN** the catalog SHALL continue to report the pre-modification content until the main-agent is restarted

### Requirement: Diagnostic logging on init
The skills store SHALL emit one structured INFO log line on successful initialization summarizing the catalog.

#### Scenario: Init summary logged
- **WHEN** the store finishes scanning
- **THEN** it SHALL log at INFO with `msg: "skills_store_initialized"` and fields `{root, skill_count, skipped_count}` where `skipped_count` is the number of directories that contained a `SKILL.md` but were skipped due to invalid frontmatter or missing required fields
