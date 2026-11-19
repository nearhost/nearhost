package elevate

import "context"

// turn lets one holder at a time through, and lets a waiter give up when its
// context ends, which a sync.Mutex cannot. It is a buffered channel of size
// one: a value in the channel is the turn, held by whoever took it out.
type turn chan struct{}

// newTurn returns a free turn.
func newTurn() turn {
	t := make(turn, 1)
	t <- struct{}{}
	return t
}

// acquire waits for the turn and returns nil once the caller holds it, or
// returns ctx's error when ctx ends first. A context that has already ended
// never takes the turn, even when it is free.
func (t turn) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-t:
		// The turn and the end of ctx can come together; a caller whose
		// context has ended must not leave holding the turn.
		if err := ctx.Err(); err != nil {
			t.release()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release gives the turn back. Only the holder calls it, once.
func (t turn) release() { t <- struct{}{} }
