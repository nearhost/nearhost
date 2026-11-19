package elevate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTurnLetsOneHolderThrough(t *testing.T) {
	tn := newTurn()
	if err := tn.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() { got <- tn.acquire(context.Background()) }()
	select {
	case err := <-got:
		t.Fatalf("second acquire returned %v while the turn was held", err)
	case <-time.After(100 * time.Millisecond):
	}
	tn.release()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire did not get the turn after release")
	}
}

func TestTurnWaiterGivesUpWhenItsContextEnds(t *testing.T) {
	tn := newTurn()
	if err := tn.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := tn.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire: %v, want DeadlineExceeded", err)
	}
	// The giving-up waiter took nothing: one release frees the turn once.
	tn.release()
	if err := tn.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTurnEndedContextNeverTakesAFreeTurn(t *testing.T) {
	tn := newTurn()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 20 {
		if err := tn.acquire(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire: %v, want Canceled", err)
		}
	}
	if err := tn.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A waiter that wins the turn as its context ends either holds the turn with
// a nil error or leaves the turn free; it never keeps the turn and errs.
func TestTurnAcquireThatErrsLeavesTheTurnFree(t *testing.T) {
	for range 200 {
		tn := newTurn()
		if err := tn.acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan error, 1)
		go func() { got <- tn.acquire(ctx) }()
		cancel()
		tn.release()
		err := <-got
		if err == nil {
			tn.release()
		}
		free, stop := context.WithTimeout(context.Background(), 2*time.Second)
		if e := tn.acquire(free); e != nil {
			t.Fatalf("turn not free after acquire returned %v: %v", err, e)
		}
		stop()
	}
}
