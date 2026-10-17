package history

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Agents serving one site record into one passed list at once; no
// recorded id may be lost to another agent's write.
func TestRecordPassedConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passed.json")
	legacy := filepath.Join(dir, "legacy-passed.json")
	if err := writeList(legacy, webPassed{Conversations: map[string]time.Time{"conv-old": time.Now()}}); err != nil {
		t.Fatal(err)
	}
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := range n {
		wg.Go(func() {
			if err := migratePassed(path, legacy); err != nil {
				errs <- err
			}
			if err := recordPassed(path, fmt.Sprintf("conv-%d", i), time.Now()); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	passed, err := loadPassed(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(passed) != n+1 {
		t.Fatalf("passed list holds %d ids, want %d", len(passed), n+1)
	}
}
