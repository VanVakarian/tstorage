package tstorage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A late correction for a point living in a closed partition is force-inserted
// into the head, stretching the head's range back over older partitions. After
// a restart partitions are re-sorted by min timestamp, so that head lands
// *before* the partitions between it and its old start — Select must still
// find its newest points.
func Test_storage_Select_overlappingPartitionsAfterRestart(t *testing.T) {
	dir := t.TempDir()
	open := func() Storage {
		s, err := NewStorage(WithDataPath(dir), WithPartitionDuration(time.Hour), WithTimestampPrecision(Seconds))
		assert.NoError(t, err)
		return s
	}
	insert := func(s Storage, ts int64, value float64) {
		assert.NoError(t, s.InsertRows([]Row{{Metric: "m", DataPoint: DataPoint{Timestamp: ts, Value: value}}}))
	}
	values := func(s Storage, start, end int64) map[int64]float64 {
		points, err := s.Select("m", nil, start, end)
		if err != nil {
			assert.ErrorIs(t, err, ErrNoDataPoints)
		}
		got := make(map[int64]float64, len(points))
		for _, p := range points {
			got[p.Timestamp] = p.Value // later partition wins on duplicate timestamps
		}
		return got
	}

	// Three closed partitions: A (10..13), C (50..70), B (100..130).
	for _, group := range [][]int64{{10, 11, 12, 13}, {50, 60, 70}, {100, 110, 120, 130}} {
		s := open()
		for _, ts := range group {
			insert(s, ts, float64(ts))
		}
		assert.NoError(t, s.Close())
	}

	s := open()
	insert(s, 140, 140) // fresh live point in the new head
	insert(s, 11, 999)  // correction of a point in closed partition A
	assert.NoError(t, s.Close())

	s = open()
	defer s.Close()
	assert.Equal(t, map[int64]float64{140: 140}, values(s, 135, 200), "live point must stay visible")
	assert.Equal(t, map[int64]float64{120: 120, 130: 130, 140: 140}, values(s, 115, 200))
	assert.Equal(t, float64(999), values(s, 0, 200)[11], "correction must win over the original value")
}

// A correction for the very FIRST point of a partition lands in a new partition
// with an identical minTimestamp. On reload the tie must resolve by creation
// time — later write last — or the stale original wins on the duplicate
// timestamp. Repeated corrections of the same point must each win in turn.
func Test_storage_Select_correctionOfPartitionFirstPointWins(t *testing.T) {
	dir := t.TempDir()
	open := func() Storage {
		s, err := NewStorage(WithDataPath(dir), WithPartitionDuration(time.Hour), WithTimestampPrecision(Seconds))
		assert.NoError(t, err)
		return s
	}
	insert := func(s Storage, ts int64, value float64) {
		assert.NoError(t, s.InsertRows([]Row{{Metric: "m", DataPoint: DataPoint{Timestamp: ts, Value: value}}}))
	}
	valueAt := func(ts int64) float64 {
		s := open()
		defer s.Close()
		points, err := s.Select("m", nil, 0, 1_000_000)
		assert.NoError(t, err)
		value := float64(-1)
		for _, p := range points {
			if p.Timestamp == ts {
				value = p.Value // later partition wins on duplicate timestamps
			}
		}
		return value
	}

	// Enough closed partitions that the reload sort is not a trivial insertion sort.
	for i := int64(1); i <= 20; i++ {
		s := open()
		insert(s, 1000*i, float64(i))
		insert(s, 1000*i+10, float64(i))
		assert.NoError(t, s.Close())
	}

	for round, value := range []float64{111, 222, 333} {
		s := open()
		insert(s, 5000, value)             // first point of its partition
		insert(s, 900_000+int64(round), 1) // keeps each correction's partition distinct
		assert.NoError(t, s.Close())
		assert.Equal(t, value, valueAt(5000), "correction #%d must win", round+1)
	}
}
