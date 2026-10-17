//go:build !unix

package history

// lockList is a no-op where flock is not available; list writes there
// are not serialized between processes.
func lockList(string) (func(), error) { return func() {}, nil }
