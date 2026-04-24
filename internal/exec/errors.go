package exec

import "errors"

// Sentinel errors exposed by the exec service. Handlers translate these to
// HTTP status codes; callers can errors.Is() them for programmatic handling.
var (
	ErrSkillUnknown       = errors.New("skill not found")
	ErrScriptPathEscape   = errors.New("script path escapes the skill root")
	ErrScriptPathAbsolute = errors.New("script path must be relative")
	ErrScriptNotRegular   = errors.New("script must be a regular file")
	ErrScriptMissing      = errors.New("script does not exist")
	ErrNetworkDenied      = errors.New("network policy denies this skill")
	ErrWorkspaceFull      = errors.New("workspace tmpfs is full")
	ErrInstallFailed      = errors.New("dependency install failed")
	ErrInvalidRequest     = errors.New("invalid request")
)
