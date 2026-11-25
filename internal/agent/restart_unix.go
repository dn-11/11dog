//go:build !windows

package agent

import (
	"os"
	"syscall"
)

func restartSelf(exePath string) error {
	return syscall.Exec(exePath, os.Args, os.Environ())
}
