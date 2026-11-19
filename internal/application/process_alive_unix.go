//go:build !windows

package application

import (
	"errors"
	"os"
	"syscall"
)

func processAlive(pid int64) (bool, error) {
	if pid <= 0 { return false, errors.New("invalid process ID") }
	p, err := os.FindProcess(int(pid))
	if err != nil { return false, err }
	defer p.Release()
	err = p.Signal(syscall.Signal(0))
	if err == nil { return true, nil }
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) { return false, nil }
	return false, err
}
