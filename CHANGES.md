# Fork changes

This is a fork of [nakabonne/tstorage](https://github.com/nakabonne/tstorage) used by
[flatline](https://github.com/VanVakarian/flatline). Only change on top of upstream:

## Out-of-order and same-timestamp inserts now land in their correct sorted position

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

Out-of-scope on purpose: a value that needs correcting after its partition has
already been flushed to an immutable on-disk file. That's a heavier problem (rewriting
a read-only mmap'd file) and isn't hit by flatline's own usage.

See `memory_partition.go`'s `insertPoint` for the change, `memory_partition_test.go`'s
two new cases ("overwrite existing timestamp", "insert genuinely earlier point between
existing ones") for the regression tests, and
`storage_examples_test.go`'s `ExampleStorage_Select_from_memory_out_of_order` (updated
to its corrected output).
