package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// maxRequestBodyBytes is the cap applied to /v1/run and /v1/install bodies.
// 1 MB is generous for a JSON envelope describing args + stdin.
const maxRequestBodyBytes = 1 << 20

// Server wires config, runner, installer, and health into HTTP handlers.
type Server struct {
	cfg       *Config
	runner    *Runner
	installer *Installer
	health    *Health
	logger    *slog.Logger

	skillsMu sync.RWMutex
	skills   []SkillInfo
}

// NewServer constructs a Server. skills is the initial scan snapshot (see
// ScanSkills). It can be refreshed later via ReplaceSkills if future work
// adds hot-reload; v1 scans once at boot.
func NewServer(cfg *Config, runner *Runner, installer *Installer, health *Health, logger *slog.Logger, skills []SkillInfo) *Server {
	return &Server{
		cfg:       cfg,
		runner:    runner,
		installer: installer,
		health:    health,
		logger:    logger,
		skills:    skills,
	}
}

// Handler returns the configured HTTP handler. Paths other than the three
// documented endpoints return 404.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/run", s.handleRun)
	mux.HandleFunc("/v1/install", s.handleInstall)
	mux.HandleFunc("/v1/health", s.handleHealth)
	return notFoundFallback(mux)
}

// notFoundFallback ensures unknown paths return a JSON 404, matching the
// envelope used by the real handlers.
func notFoundFallback(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/run", "/v1/install", "/v1/health":
			h.ServeHTTP(w, r)
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	})
}

// ReplaceSkills swaps the cached skill list. Not used by v1's boot-only
// scan but exposed for tests.
func (s *Server) ReplaceSkills(skills []SkillInfo) {
	s.skillsMu.Lock()
	s.skills = skills
	s.skillsMu.Unlock()
}

func (s *Server) lookupSkill(name string) (*SkillInfo, bool) {
	s.skillsMu.RLock()
	defer s.skillsMu.RUnlock()
	return LookupSkill(s.skills, name)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req RunRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Skill == "" || req.Script == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill and script are required"})
		return
	}

	skill, ok := s.lookupSkill(req.Skill)
	if !ok {
		s.logger.Info("exec_run_rejected", "skill", req.Skill, "reason", "unknown_skill")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("skill not found: %s", req.Skill)})
		return
	}

	decision := Policy(skill.Name, s.cfg)
	if !decision.Allow {
		s.logger.Info("exec_run_rejected", "skill", skill.Name, "reason", "network_denied")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": decision.Reason})
		return
	}

	resp, err := s.runner.Run(r.Context(), &req, *skill)
	if err != nil {
		switch {
		case errors.Is(err, ErrScriptPathAbsolute),
			errors.Is(err, ErrScriptPathEscape),
			errors.Is(err, ErrScriptMissing),
			errors.Is(err, ErrScriptNotRegular),
			errors.Is(err, ErrInvalidRequest):
			s.logger.Info("exec_run_rejected", "skill", skill.Name, "reason", classifyReject(err))
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrWorkspaceFull):
			s.logger.Info("exec_run_rejected", "skill", skill.Name, "reason", "workspace_full")
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			s.logger.Error("exec_run_internal", "skill", skill.Name, "error", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func classifyReject(err error) string {
	switch {
	case errors.Is(err, ErrScriptPathAbsolute), errors.Is(err, ErrScriptPathEscape):
		return "script_escape"
	case errors.Is(err, ErrScriptMissing):
		return "script_missing"
	case errors.Is(err, ErrScriptNotRegular):
		return "script_not_regular"
	}
	return "invalid_request"
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req InstallRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Skill == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill is required"})
		return
	}
	skill, ok := s.lookupSkill(req.Skill)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("skill not found: %s", req.Skill)})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.InstallTimeout)
	defer cancel()
	res := s.installer.Install(ctx, *skill, PhaseExplicit)
	writeJSON(w, http.StatusOK, InstallResponse{
		Status:     res.Status,
		DurationMS: res.DurationMS,
		Error:      res.Error,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	snap := s.health.Snapshot()
	if snap.PrewarmedSkills == nil {
		snap.PrewarmedSkills = []string{}
	}
	sort.Strings(snap.PrewarmedSkills)
	if snap.FailedInstalls == nil {
		snap.FailedInstalls = []FailedInstall{}
	}
	writeJSON(w, http.StatusOK, snap)
}

func decodeJSONBody(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	// Reject multiple JSON values in one body.
	if dec.More() {
		return errors.New("invalid request body: trailing data after JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Run binds the configured HTTP server to cfg.Port, accepts connections,
// and blocks until ctx is cancelled. On cancel, gracefully stops within
// cfg.ShutdownGrace.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.cfg.Port),
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("exec_listening", "port", s.cfg.Port)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownGrace)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
