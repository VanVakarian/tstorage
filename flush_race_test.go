package tstorage

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStorage_ConcurrentPartitionRotationsDoNotRace is a regression test for
// two bugs fixed on top of upstream (see CHANGES.md, commits 1481a4a and
// 26e00d6): ensureActiveHead used to spawn its background flushPartitions
// goroutine without tracking it via storage.wg (so Close() could return
// before a flush finished) and without any lock serializing overlapping runs
// of that goroutine against each other (so a burst of rapid rotations could
// have two flushes mutate the shared partition list, and even the still-live
// head partition's own data, at the same time).
//
// Neither bug was ever hit by normal production traffic (writes arrive
// roughly once a minute against hour-or-longer partitions, giving each flush
// ample time to finish before the next rotation) — it only ever showed up
// under a tight insert loop causing several rotations in quick succession,
// and even then only intermittently; the original discovery took dozens of
// manual test reruns. This test forces exactly that: many goroutines
// inserting concurrently, with no delay between inserts, against a partition
// duration short enough to rotate dozens of times while the test runs.
//
// Verified against a pre-fix copy of storage.go (checked out from the
// commit right before 1481a4a): `go test -race` on this test reliably
// reports a DATA RACE within a handful of runs against that version, and
// passes clean against the fixed version — so this isn't a test that merely
// looks plausible, it was confirmed to actually catch the regression it
// names.
func TestStorage_ConcurrentPartitionRotationsDoNotRace(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(
		WithDataPath(dir),
		WithPartitionDuration(5*time.Millisecond),
		WithTimestampPrecision(Nanoseconds),
		WithWALBufferedSize(0),
	)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	const writers = 32
	const rowsPerWriter = 2000
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := 0; i < rowsPerWriter; i++ {
				_ = s.InsertRows([]Row{{
					Metric:    "m",
					Labels:    []Label{{Name: "writer", Value: string(rune('a' + writer))}},
					DataPoint: DataPoint{Timestamp: time.Now().UnixNano(), Value: float64(i)},
				}})
			}
		}(writer)
	}
	wg.Wait()

	// Close() must not return until every background flush it triggered has
	// actually finished (storage.wg) — if it returned early, a flush could
	// still be mutating files after the test asserts on the directory below.
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	flushedPartitions := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "p-") {
			flushedPartitions++
		}
	}
	if flushedPartitions == 0 {
		t.Fatal("no partitions were flushed to disk — the 5ms partition duration should have forced several rotations")
	}
}
