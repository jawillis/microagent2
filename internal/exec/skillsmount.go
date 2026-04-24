package exec

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillInfo is the minimal metadata exec needs per skill. Intentionally
// independent of internal/skills/ per design Decision 4.
type SkillInfo struct {
	Name    string
	Root    string // absolute path to the skill directory
	Prewarm bool
}

// ScanSkills walks skillsDir one level deep, parses each SKILL.md's YAML
// frontmatter, and returns info for skills whose frontmatter is valid.
// Malformed or incomplete skills are logged and skipped.
func ScanSkills(skillsDir string, logger *slog.Logger) []SkillInfo {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if logger != nil && !os.IsNotExist(err) {
			logger.Warn("exec_skills_scan_failed", "dir", skillsDir, "error", err.Error())
		}
		return nil
	}

	out := make([]SkillInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillRoot := filepath.Join(skillsDir, e.Name())
		skillPath := filepath.Join(skillRoot, "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		fm, err := parseExecFrontmatter(data)
		if err != nil {
			if logger != nil {
				logger.Warn("exec_skill_frontmatter_invalid", "path", skillPath, "error", err.Error())
			}
			continue
		}
		name := strings.TrimSpace(fm.Name)
		if name == "" {
			continue
		}
		out = append(out, SkillInfo{
			Name:    name,
			Root:    skillRoot,
			Prewarm: fm.XMicroagent.Prewarm,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LookupSkill returns the SkillInfo for name or (nil, false) if unknown.
// Intended for request-time validation; callers pass the most recent scan
// result rather than rescanning.
func LookupSkill(skills []SkillInfo, name string) (*SkillInfo, bool) {
	for i := range skills {
		if skills[i].Name == name {
			return &skills[i], true
		}
	}
	return nil, false
}

type execFrontmatter struct {
	Name         string `yaml:"name"`
	XMicroagent  struct {
		Prewarm bool `yaml:"prewarm"`
	} `yaml:"x-microagent"`
}

// parseExecFrontmatter is a minimal SKILL.md frontmatter reader that extracts
// only the fields exec cares about. Separate from internal/skills/ by
// design — exec is a service, not an importer of agent state.
func parseExecFrontmatter(contents []byte) (*execFrontmatter, error) {
	normalized := bytes.ReplaceAll(contents, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) && !bytes.HasPrefix(normalized, []byte("---\r\n")) {
		// Allow a leading "---" with only a newline after.
		trim := bytes.TrimPrefix(normalized, []byte("---"))
		if len(trim) == len(normalized) {
			return nil, errFrontmatterMissing
		}
		normalized = append([]byte("---\n"), trim...)
	}
	rest := normalized[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, errFrontmatterUnterminated
	}
	var fm execFrontmatter
	if err := yaml.Unmarshal(rest[:end], &fm); err != nil {
		return nil, err
	}
	return &fm, nil
}

// Sentinel errors local to frontmatter parsing.
var (
	errFrontmatterMissing     = osError("skill frontmatter missing opening ---")
	errFrontmatterUnterminated = osError("skill frontmatter missing closing ---")
)

// osError is an error type constructor that avoids importing errors here
// (keeps this file's imports small).
type osError string

func (e osError) Error() string { return string(e) }
