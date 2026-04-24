//go:build unix

package exec

import (
	"strconv"
	"syscall"
)

// syscallKillZero sends signal 0 to check whether a PID is alive. Returns
// nil when the process still exists, error otherwise.
func syscallKillZero(pidStr string) error {
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return err
	}
	return syscall.Kill(pid, 0)
}
