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

### Requirement: Skill root directory tracking
Each skill's `Manifest` SHALL record the absolute path of the directory that contains its `SKILL.md`. This path is set at scan time from the discovered `SKILL.md` location and SHALL be exposed via a `Root()` method on the `Manifest`. The root directory is the sandbox boundary for subsequent file-access operations.

#### Scenario: Root recorded at scan time
- **WHEN** the skills store finishes scanning a root that contains `skills/code-review/SKILL.md`
- **THEN** `store.Get("code-review")` SHALL return a `Manifest` whose `Root()` returns the absolute path to the `code-review` directory (equivalent to `filepath.Dir(sourcePath)`)

#### Scenario: Root is independent of source path
- **WHEN** the scan encounters `SKILL.md` inside a symlinked parent directory
- **THEN** `Root()` SHALL return the unresolved directory path that was enumerated during the scan; symlink resolution SHALL happen at file-access time, not at scan time

### Requirement: Skill-relative file access
The skills store SHALL expose a `ReadFile(name, relPath) (contents string, found bool, err error)` method that reads a file inside a skill's directory. Its behavior is:
- `found == false`, `err == nil` when no skill is registered under `name`.
- `found == true`, `err != nil` when the skill exists but the path is rejected for any reason below.
- `found == true`, `err == nil` when the read succeeds; `contents` is the full file bytes as a string.

The method SHALL enforce the following validation rules before touching the filesystem:
1. Reject absolute paths.
2. Reject paths that, after `filepath.Clean`, contain any `..` segment or are equal to `..`.
3. Reject the reserved path `SKILL.md` (served by existing catalog methods, not by `ReadFile`).

After validation, the method SHALL resolve symlinks on both the requested path (`filepath.Join(root, cleanPath)`) and on the skill root, then verify the resolved target lies within the resolved root (using a trailing-separator prefix check) before opening the file.

The method SHALL also reject:
- Paths that do not resolve to a regular file (directories, symlinks to directories, sockets, pipes, devices, etc.).
- Files whose on-disk size exceeds `SKILL_FILE_MAX_BYTES` (default 256 KB).
- Paths that do not exist.

#### Scenario: Valid relative path returns contents
- **WHEN** `ReadFile("code-review", "language-notes.md")` is called and the file exists, is a regular file, and is within the size cap
- **THEN** the call SHALL return `(contents, true, nil)` where `contents` is the exact byte content of the file

#### Scenario: Unknown skill
- **WHEN** `ReadFile("nonexistent-skill", "anything.md")` is called and no such skill is registered
- **THEN** the call SHALL return `("", false, nil)` — no filesystem access is attempted

#### Scenario: Absolute path rejected
- **WHEN** `ReadFile("code-review", "/etc/passwd")` is called
- **THEN** the call SHALL return `("", true, err)` where `err` indicates the path must be relative

#### Scenario: Parent-directory traversal rejected
- **WHEN** `ReadFile("code-review", "../other-skill/SKILL.md")` is called
- **THEN** the call SHALL return `("", true, err)` where `err` indicates the path escapes the skill root
- **AND** no file read SHALL be attempted, even if the target file exists

#### Scenario: Cleaned path still containing `..` rejected
- **WHEN** `ReadFile` is given a path that `filepath.Clean` leaves containing a `..` segment (platform-dependent edge case)
- **THEN** the call SHALL return `("", true, err)` with the same error class as direct traversal

#### Scenario: Reserved SKILL.md rejected
- **WHEN** `ReadFile("code-review", "SKILL.md")` is called
- **THEN** the call SHALL return `("", true, err)` where `err` directs the caller to use `Body` / `read_skill` instead

#### Scenario: Symlink inside root allowed
- **WHEN** `ReadFile("code-review", "linked.md")` is called and `linked.md` is a symlink whose resolved target is another file inside `skills/code-review/`
- **THEN** the call SHALL return `(contents, true, nil)` with the target file's contents

#### Scenario: Symlink escaping root rejected
- **WHEN** `ReadFile("code-review", "escape.md")` is called and `escape.md` is a symlink whose resolved target is outside the skill root
- **THEN** the call SHALL return `("", true, err)` where `err` indicates the resolved path is outside the skill root

#### Scenario: Non-regular file rejected
- **WHEN** `ReadFile("code-review", "some-subdir")` is called and the path resolves to a directory, socket, pipe, or device
- **THEN** the call SHALL return `("", true, err)` where `err` indicates the target is not a regular file

#### Scenario: Oversize file rejected
- **WHEN** `ReadFile("code-review", "huge.md")` is called and the file's on-disk size exceeds `SKILL_FILE_MAX_BYTES`
- **THEN** the call SHALL return `("", true, err)` where `err` reports the file size and the configured cap
- **AND** the file SHALL NOT be read into memory

#### Scenario: Nonexistent file within valid skill
- **WHEN** `ReadFile("code-review", "missing.md")` is called and the skill exists but the file does not
- **THEN** the call SHALL return `("", true, err)` where `err` is a structured "file not found" signal (distinct from the "skill not found" case, which returns `found == false`)

### Requirement: Configurable file-read size cap
The skills store SHALL read `SKILL_FILE_MAX_BYTES` from the environment on initialization and use it as the per-file size cap for `ReadFile`. When unset or not parseable as a positive integer, the default SHALL be 262144 (256 KB).

#### Scenario: Default cap
- **WHEN** `SKILL_FILE_MAX_BYTES` is not set in the environment
- **THEN** the effective cap SHALL be 262144 bytes

#### Scenario: Configured cap honored
- **WHEN** `SKILL_FILE_MAX_BYTES=1048576` is set
- **THEN** the effective cap SHALL be 1048576 bytes and files up to that size SHALL be readable

#### Scenario: Invalid cap falls back
- **WHEN** `SKILL_FILE_MAX_BYTES` is set to a non-numeric or non-positive value
- **THEN** the effective cap SHALL be the 262144-byte default and initialization SHALL NOT fail
