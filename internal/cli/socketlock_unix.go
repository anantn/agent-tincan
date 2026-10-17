//go:build unix

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// tryLock takes the shared record's lock without waiting. It reports false
// when another window holds it. The lock is an OS file lock, so a window
// that crashes while holding it frees it.
func (s socketShared) tryLock() (func(), bool, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, agentFileName(s.agent)+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true, nil
}

// processAlive reports whether pid names a running process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
