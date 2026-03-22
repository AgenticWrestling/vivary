package switchboard

import "sync/atomic"

// SeqCounter is a monotonically increasing sequence number generator.
// Safe for concurrent use.
type SeqCounter struct {
	n atomic.Uint64
}

// Next returns the next sequence number (starting at 1).
func (s *SeqCounter) Next() uint64 {
	return s.n.Add(1)
}
