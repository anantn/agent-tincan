//go:build !unix

package cli

import "errors"

// tryLock fails where there are no unix file locks. Socket wake needs a
// unix socket, so it never runs there.
func (s socketShared) tryLock() (func(), bool, error) {
	return nil, false, errors.New("socket wake needs unix file locks")
}

func processAlive(int) bool { return false }
