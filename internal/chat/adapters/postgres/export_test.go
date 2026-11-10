package postgres

// WithBetweenReads returns a Store whose Tail runs f between its two reads.
func WithBetweenReads(s Store, f func()) Store {
	s.betweenReads = f
	return s
}
