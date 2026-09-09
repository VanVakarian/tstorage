# Fork changes

This is a fork of [nakabonne/tstorage](https://github.com/nakabonne/tstorage) used by
[flatline](https://github.com/VanVakarian/flatline). Changes on top of upstream:

## 1. Out-of-order and same-timestamp inserts now land in their correct sorted position

Upstream's `memoryMetric.insertPoint` only ever appends: a point whose timestamp is
greater than the last one goes onto the end of the sorted `points` slice; anything
else — a same-timestamp overwrite, or a genuinely new but chronologically earlier
point (e.g. backfilled historical data arriving after newer data already flowed
through) — gets pushed onto a separate `outOfOrderPoints` slice instead. The read path
(`selectPoints`) only ever looks at `points`, so none of that data was visible until
the partition flushed to disk — and even then it didn't resolve correctly for the
overwrite case, since `encodeAllPoints` merges both slices by timestamp without
deduplicating, leaving the old and new value as two separate points sharing the same
timestamp forever.

This fork replaces the `outOfOrderPoints` fallback with a proper sorted insert: a
binary search (`points` is already sorted) finds where the new point belongs — an
exact timestamp match replaces that entry in place, anything else is inserted at the
right position, shifting later elements up. `outOfOrderPoints`/`encodeAllPoints`'s
merge are left in place as dead-but-harmless code (nothing appends to
`outOfOrderPoints` anymore) rather than removed, to keep the diff minimal.

See `memory_partition.go`'s `insertPoint` for the change, `memory_partition_test.go`'s
two new cases ("overwrite existing timestamp", "insert genuinely earlier point between
existing ones") for the regression tests, and
`storage_examples_test.go`'s `ExampleStorage_Select_from_memory_out_of_order` (updated
to its corrected output).

## 2. A row no partition accepts is no longer silently dropped

`storage.InsertRows` tries a row against the head partition, then up to
`writablePartitionsNum` (2) older ones; upstream's own comment said the quiet part out
loud: "any rows more than writablePartitionsNum partitions out of date are dropped."
A restore/backfill for a period older than every partition currently willing to accept
it — a fresh store with only one (head) partition so far is enough to trigger this —
lost the data with no error at all.

This fork adds a last-resort fallback: if a row is still unaccepted after the normal
attempts, `memoryPartition.forceInsertRows` inserts it into the head partition anyway,
lowering that partition's tracked minimum timestamp to match instead of bouncing the
row again. Trade-off, and it's a real one: if the row's rightful partition has already
been flushed to an immutable on-disk file, force-inserting into the head widens the
head's range enough to overlap that older, already-written file — `storage.Select`'s
per-partition merge assumes partitions cover non-overlapping ranges to come back
globally sorted by timestamp, so a value backfilled that late will still be present in
full, just not necessarily returned in timestamp order. This case does not come up in
flatline's own usage (nothing corrects a value old enough to have already been flushed
to disk), so it's accepted as-is rather than fixed — a full fix would need a real
merge in `Select` and isn't worth the complexity for a path nothing exercises.

See `storage.go`'s `InsertRows` and `memory_partition.go`'s `forceInsertRows` for the
change, and `storage_examples_test.go`'s `ExampleStorage_InsertRows_outdated` (now
recovers the row cleanly, in order — it wasn't hitting the overlap case) and
`ExampleStorage_InsertRows_expired` (does hit the overlap case, updated to its
corrected-but-reordered output, with the trade-off spelled out in the comment).

## 3. The background flush goroutine could run detached and could race itself

`storage.ensureActiveHead` spawns a goroutine to flush newly-inactive partitions to
disk every time it rotates in a new head partition. Upstream fired this off
completely detached: not tracked by `storage.wg` (the wait group `Close()` waits on),
and with nothing stopping two rotations in quick succession from running
`flushPartitions` at the same time against the same partition list.

Neither half of this ever showed up under normal traffic (writes roughly once a
minute against hour-plus partitions leave a flush ample time to finish long before
the next rotation), but a tight insert loop — the kind a stress test or a burst
backfill produces — could trigger it, and did: intermittent, non-deterministic test
failures across different runs, the kind of flakiness that's easy to mistake for
something else. It took dozens of manual reruns to pin down.

This fork tracks the goroutine via `storage.wg` (so `Close()` actually waits for it)
and added `storage.flushMu`, a plain mutex serializing overlapping runs against each
other — `flushPartitions` is idempotent, so a queued-up run simply finds nothing left
to do if a predecessor already handled it.

See `storage.go`'s `ensureActiveHead` for the change.

## 4. `memoryMetric.encodeAllPoints` and `storage.flush` read partition state without the lock that protects it

Fixing #3 above closed the *known* concurrency issue, but writing a regression test
that actually forces many rapid rotations under `go test -race` (rather than relying
on manual reruns to notice something odd) surfaced a second, unrelated race that
predates this fork entirely: `flushPartitions` only skips the first
`writablePartitionsNum` (2) partitions *as its own iterator sees the list at the
moment it runs* — under a fast enough burst of rotations, a partition an in-flight
`InsertRows` call still targets (via the writablePartitionsNum fallback loop, using a
snapshot iterator taken earlier) can already have been pushed past that boundary by
the time a flush run gets to it. Two consequences, confirmed with `go test -race`
under a stress test (many concurrent writers, a 5ms partition duration — see
`flush_race_test.go`), not assumed:

- `memoryMetric.encodeAllPoints` (called only from the flush path) read and — via its
  `sort.Slice` on `outOfOrderPoints` — mutated `points`/`outOfOrderPoints` without
  ever taking `memoryMetric.mu`, the same lock `insertPoint` (write) and
  `selectPoints` (read) both already respect. Not a design choice, just a missing
  lock.
- `storage.flush`'s own loop read `mt.size`, `mt.minTimestamp`, and `mt.maxTimestamp`
  as plain struct fields, racing against `insertPoint`'s `atomic.Store/AddInt64`
  writes to those same fields — the read side needs `atomic.LoadInt64` too, same as
  `selectPoints` already does it.

Verified the regression test actually catches this: run against a version of
`storage.go` from right before fix #3's commit (which still has neither `encodeAllPoints`
fix), `go test -race` reliably reports the race within a handful of runs; clean
against the fixed version across 20+ repeats.

Not a production concern at flatline's real write rate (no realistic way to fit
several full partition rotations inside one in-flight `InsertRows` call when writes
arrive roughly once a minute against hour-plus partitions) — but a real, pre-existing
bug in the library regardless of how rarely conditions line up to hit it.

See `memory_partition.go`'s `encodeAllPoints` and `storage.go`'s `flush` for the
change, and `flush_race_test.go` for the regression test.
