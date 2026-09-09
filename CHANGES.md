# Fork changes

This is a fork of [nakabonne/tstorage](https://github.com/nakabonne/tstorage) used by
[flatline](https://github.com/VanVakarian/flatline). Only change on top of upstream:

## Same-timestamp inserts now overwrite in place

Upstream's `memoryMetric.insertPoint` only ever appends: a point whose timestamp is
greater than the last one goes onto the end of the sorted `points` slice; anything
else — including a second insert at a timestamp that's already present — gets pushed
onto a separate `outOfOrderPoints` slice instead. The read path (`selectPoints`) only
ever looks at `points`, so an intentional overwrite (recomputing an aggregate once
more source data has arrived, for example) was silently invisible until the partition
flushed to disk — and even then it didn't resolve into one correct value, since
`encodeAllPoints` merges both slices by timestamp without deduplicating, leaving both
the old and new value as separate points sharing the same timestamp.

This fork adds an exact-timestamp check (binary search, since `points` is already
sorted) before falling back to `outOfOrderPoints`: if the new point's timestamp
matches an existing one, it replaces that entry in place instead.

Out-of-scope on purpose: a value that needs correcting after its partition has
already been flushed to an immutable on-disk file. That's a heavier problem (rewriting
a read-only mmap'd file) and isn't hit by flatline's own usage.

See `memory_partition.go`'s `insertPoint` for the change and
`memory_partition_test.go`'s "overwrite existing timestamp" case for the regression
test.
