package github

import (
	"context"
	"io"
	"sync"
)

// sliceIterator is the slice-backed Iterator[T] returned by GitHub adapter
// methods that have not yet been migrated to true cursor-driven streaming.
// It captures an eager List* result and yields entries one at a time, so
// callers can use the forge.Iterator surface without behavioural change.
//
// A construction error (when the underlying List* call failed) is surfaced
// on the first Next() invocation, matching the iterator contract that
// non-EOF errors stop iteration.
type sliceIterator[T any] struct {
	mu     sync.Mutex
	items  []T
	err    error
	idx    int
	closed bool
}

func newSliceIterator[T any](items []T, err error) *sliceIterator[T] {
	return &sliceIterator[T]{items: items, err: err}
}

// Next returns the next item, io.EOF when exhausted, or the construction
// error when the underlying List* call failed.
func (it *sliceIterator[T]) Next(_ context.Context) (*T, error) {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.closed {
		return nil, io.EOF
	}
	if it.err != nil {
		err := it.err
		it.err = nil
		return nil, err
	}
	if it.idx >= len(it.items) {
		return nil, io.EOF
	}
	v := it.items[it.idx]
	it.idx++
	return &v, nil
}

// Close marks the iterator as exhausted. Idempotent.
func (it *sliceIterator[T]) Close() error {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.closed = true
	return nil
}
