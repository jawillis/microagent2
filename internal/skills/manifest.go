package skills

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Name         string
	Description  string
	AllowedTools []string
	Model        string
	body         string
	sourcePath   string
	rootDir      string
}

func (m *Manifest) Body() string       { return m.body }
func (m *Manifest) SourcePath() string { return m.sourcePath }
func (m *Manifest) Root() string       { return m.rootDir }

type frontmatterFields struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	AllowedTools []string `yaml:"allowed-tools"`
	Model        string   `yaml:"model"`
}

// parseFrontmatter splits a SKILL.md file into its YAML frontmatter block and
// Markdown body. The file must open with a line containing only `---`
// (optionally followed by CR), followed by YAML lines, followed by a closing
// `---` line. Everything after the closing `---` (minus a single leading blank
// line, if any) is the body.
func parseFrontmatter(contents []byte) (*frontmatterFields, string, error) {
	normalized := bytes.ReplaceAll(contents, []byte("\r\n"), []byte("\n"))
	lines := bytes.SplitN(normalized, []byte("\n"), 2)
	if len(lines) < 2 {
		return nil, "", fmt.Errorf("no frontmatter delimiter")
	}
	if strings.TrimSpace(string(lines[0])) != "---" {
		return nil, "", fmt.Errorf("file does not start with --- delimiter")
	}
	rest := lines[1]

	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, "", fmt.Errorf("no closing --- delimiter")
	}
	fmBytes := rest[:end]

	// Advance past the closing --- and its newline(s).
	afterDelim := rest[end+len("\n---"):]
	// Drop any trailing chars on that closing line.
	if nl := bytes.IndexByte(afterDelim, '\n'); nl >= 0 {
		afterDelim = afterDelim[nl+1:]
	} else {
		afterDelim = nil
	}
	// Trim a single leading blank line if present.
	body := string(afterDelim)
	body = strings.TrimLeft(body, "\n")

	var fm frontmatterFields
	if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
		return nil, "", fmt.Errorf("yaml parse: %w", err)
	}
	return &fm, body, nil
}
