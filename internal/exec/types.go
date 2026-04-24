package exec

// RunRequest is the decoded body of POST /v1/run.
type RunRequest struct {
	Skill     string   `json:"skill"`
	Script    string   `json:"script"`
	Args      []string `json:"args,omitempty"`
	Stdin     string   `json:"stdin,omitempty"`
	TimeoutS  int      `json:"timeout_s,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
}

// OutputFile describes a non-hidden file at the top of the workspace after
// a run completes.
type OutputFile struct {
	Path  string `json:"path"`
	MIME  string `json:"mime"`
	Bytes int64  `json:"bytes"`
}

// RunResponse is the JSON envelope returned by POST /v1/run.
type RunResponse struct {
	ExitCode          int          `json:"exit_code"`
	Stdout            string       `json:"stdout"`
	StdoutTruncated   bool         `json:"stdout_truncated"`
	Stderr            string       `json:"stderr"`
	StderrTruncated   bool         `json:"stderr_truncated"`
	WorkspaceDir      string       `json:"workspace_dir"`
	Outputs           []OutputFile `json:"outputs"`
	DurationMS        int64        `json:"duration_ms"`
	TimedOut          bool         `json:"timed_out"`
	InstallDurationMS int64        `json:"install_duration_ms"`
}

// InstallRequest is the decoded body of POST /v1/install.
type InstallRequest struct {
	Skill string `json:"skill"`
}

// InstallResponse is the envelope for POST /v1/install.
type InstallResponse struct {
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// FailedInstall is one entry in the health response.
type FailedInstall struct {
	Skill string `json:"skill"`
	Error string `json:"error"`
	At    string `json:"at"`
}

// HealthResponse is the envelope for GET /v1/health.
type HealthResponse struct {
	Status          string          `json:"status"`
	Ready           bool            `json:"ready"`
	PrewarmedSkills []string        `json:"prewarmed_skills"`
	FailedInstalls  []FailedInstall `json:"failed_installs"`
}

// BashRequest is the decoded body of POST /v1/bash.
type BashRequest struct {
	Command   string `json:"command"`
	SessionID string `json:"session_id"`
	TimeoutS  int    `json:"timeout_s,omitempty"`
}

// BashResponse is the envelope for POST /v1/bash.
type BashResponse struct {
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	Stderr          string `json:"stderr"`
	StderrTruncated bool   `json:"stderr_truncated"`
	SandboxDir      string `json:"sandbox_dir"`
	DurationMS      int64  `json:"duration_ms"`
	TimedOut        bool   `json:"timed_out"`
}
