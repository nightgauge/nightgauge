package forge

import "context"

// Iterator yields a stream of values of type T. Adapters return an Iterator
// from List-style operations that can paginate without loading the full
// result set into memory.
//
// Contract:
//   - Next returns (*T, nil) for each value, then (nil, io.EOF) when the
//     stream is exhausted. Any other error stops iteration; callers should
//     not call Next again after a non-EOF error.
//   - Close releases adapter-side resources (open HTTP responses, paginated
//     cursors). Close is idempotent — calling it multiple times returns nil
//     after the first call. Callers should always call Close, typically via
//     defer, even after Next returned io.EOF.
//   - Iterator is not safe for concurrent use; callers must serialise Next
//     calls.
type Iterator[T any] interface {
	// Next returns the next value in the stream, or (nil, io.EOF) when no
	// more values are available.
	Next(ctx context.Context) (*T, error)

	// Close releases adapter resources. Idempotent.
	Close() error
}
