//go:build unix

package history

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// listLockWait bounds how long a writer waits for another process to
// release a list's lock.
const listLockWait = 5 * time.Second

// lockList takes an advisory lock on the list at path (through path.lock
// beside it) and returns the function that releases it. Agents in several
// processes that read, change and write the same list hold it so one does
// not drop another's entry. It gives up after listLockWait.
func lockList(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(listLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, errors.New("another process holds the list's lock")
			}
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
