package skills

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultFileMaxBytes = 262144
	fileMaxBytesEnv     = "SKILL_FILE_MAX_BYTES"
)

// Error sentinels for ReadFile. The tool wrapper translates these into JSON
// envelopes; callers can also errors.Is them for programmatic classification.
var (
	ErrPathAbsolute      = errors.New("path must be relative")
	ErrPathEscapesRoot   = errors.New("path escapes the skill root")
	ErrPathReservedSKILL = errors.New("SKILL.md is served by read_skill, not read_skill_file")
	ErrNotRegularFile    = errors.New("target is not a regular file")
	ErrFileNotFound      = errors.New("file not found within skill")
)

type Store struct {
	manifests    map[string]*Manifest
	order        []string
	fileMaxBytes int64
}

// NewStore scans the given root one level deep for subdirectories containing
// a SKILL.md file. Unreadable roots, malformed frontmatter, and skills
// missing required fields are logged and skipped; the store is never fatal.
func NewStore(root string, logger *slog.Logger) *Store {
	s := &Store{
		manifests:    map[string]*Manifest{},
		fileMaxBytes: resolveFileMaxBytes(logger),
	}

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
			rootDir:      filepath.Dir(skillPath),
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

// resolveFileMaxBytes reads SKILL_FILE_MAX_BYTES, returning the default when
// unset or when the value is not a positive integer. Malformed values are
// logged at WARN but do not block initialization.
func resolveFileMaxBytes(logger *slog.Logger) int64 {
	raw := os.Getenv(fileMaxBytesEnv)
	if raw == "" {
		return defaultFileMaxBytes
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		logger.Warn("skill_file_max_bytes_invalid", "value", raw, "default", defaultFileMaxBytes)
		return defaultFileMaxBytes
	}
	return n
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

// FileMaxBytes returns the per-file size cap enforced by ReadFile.
func (s *Store) FileMaxBytes() int64 { return s.fileMaxBytes }

// ReadFile returns the contents of a file at relPath inside the named skill's
// directory. The three-valued return distinguishes three outcomes:
//
//	("",   false, nil)  — no skill registered under `name`.
//	("",   true,  err)  — skill exists; path was rejected or file unreadable.
//	(body, true,  nil)  — success; body holds the file bytes as a string.
//
// Validation rejects absolute paths, traversal, the reserved SKILL.md name,
// paths that resolve outside the skill root (including via symlinks),
// non-regular files, and files larger than the configured cap.
func (s *Store) ReadFile(name, relPath string) (string, bool, error) {
	m, ok := s.manifests[name]
	if !ok {
		return "", false, nil
	}

	if relPath == "" {
		return "", true, errors.New("path is required")
	}
	if filepath.IsAbs(relPath) {
		return "", true, ErrPathAbsolute
	}

	clean := filepath.Clean(relPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == "." {
		return "", true, ErrPathEscapesRoot
	}
	// Defensive: split and scan for any ".." segment. filepath.Clean should
	// have collapsed these but platform/edge-case variance argues for belt
	// and suspenders.
	for _, seg := range strings.Split(clean, string(filepath.Separator)) {
		if seg == ".." {
			return "", true, ErrPathEscapesRoot
		}
	}
	if clean == "SKILL.md" {
		return "", true, ErrPathReservedSKILL
	}

	rootResolved, err := filepath.EvalSymlinks(m.rootDir)
	if err != nil {
		return "", true, fmt.Errorf("resolve skill root: %w", err)
	}
	target := filepath.Join(m.rootDir, clean)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true, ErrFileNotFound
		}
		return "", true, fmt.Errorf("resolve path: %w", err)
	}

	// Prefix comparison must include a trailing separator so that
	// /skills/foo does not match /skills/foobar.
	rootWithSep := rootResolved + string(filepath.Separator)
	if resolved != rootResolved && !strings.HasPrefix(resolved+string(filepath.Separator), rootWithSep) {
		return "", true, ErrPathEscapesRoot
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true, ErrFileNotFound
		}
		return "", true, fmt.Errorf("stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", true, ErrNotRegularFile
	}
	if info.Size() > s.fileMaxBytes {
		return "", true, fmt.Errorf("file too large: %d bytes, max %d", info.Size(), s.fileMaxBytes)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", true, fmt.Errorf("read file: %w", err)
	}
	return string(data), true, nil
}
