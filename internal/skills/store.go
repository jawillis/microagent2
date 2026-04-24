package skills

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Store struct {
	manifests map[string]*Manifest
	order     []string
}

// NewStore scans the given root one level deep for subdirectories containing
// a SKILL.md file. Unreadable roots, malformed frontmatter, and skills
// missing required fields are logged and skipped; the store is never fatal.
func NewStore(root string, logger *slog.Logger) *Store {
	s := &Store{manifests: map[string]*Manifest{}}

	entries, err := os.ReadDir(root)
	if err != nil {
		logger.Warn("skills_dir_unreadable", "path", root, "error", err.Error())
		logger.Info("skills_store_initialized", "root", root, "skill_count", 0, "skipped_count", 0)
		return s
	}

	skipped := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillPath := filepath.Join(root, e.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		fm, body, err := parseFrontmatter(data)
		if err != nil {
			logger.Warn("skill_frontmatter_parse_failed", "path", skillPath, "error", err.Error())
			skipped++
			continue
		}
		if strings.TrimSpace(fm.Name) == "" || strings.TrimSpace(fm.Description) == "" {
			logger.Warn("skill_manifest_invalid", "path", skillPath, "reason", "missing name or description")
			skipped++
			continue
		}
		m := &Manifest{
			Name:         strings.TrimSpace(fm.Name),
			Description:  strings.TrimSpace(fm.Description),
			AllowedTools: fm.AllowedTools,
			Model:        fm.Model,
			body:         body,
			sourcePath:   skillPath,
		}
		if _, dup := s.manifests[m.Name]; dup {
			logger.Warn("skill_manifest_invalid", "path", skillPath, "reason", "duplicate name")
			skipped++
			continue
		}
		s.manifests[m.Name] = m
		s.order = append(s.order, m.Name)
	}

	sort.Strings(s.order)
	logger.Info("skills_store_initialized", "root", root, "skill_count", len(s.manifests), "skipped_count", skipped)
	return s
}

func (s *Store) List() []*Manifest {
	out := make([]*Manifest, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.manifests[name])
	}
	return out
}

func (s *Store) Get(name string) (*Manifest, bool) {
	m, ok := s.manifests[name]
	return m, ok
}

func (s *Store) Body(name string) (string, bool) {
	m, ok := s.manifests[name]
	if !ok {
		return "", false
	}
	return m.body, true
}
