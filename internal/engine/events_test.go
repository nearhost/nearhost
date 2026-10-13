package engine

import (
	"sync"
	"testing"
	"time"
)

func TestBusDeliversToEverySubscriberAndStampsTime(t *testing.T) {
	b := NewBus()
	a, unsubA := b.Subscribe(4)
	defer unsubA()
	c, unsubC := b.Subscribe(4)
	defer unsubC()
	b.Publish(Event{Project: "web", Message: "hello"})
	for _, ch := range []<-chan Event{a, c} {
		select {
		case ev := <-ch:
			if ev.Message != "hello" || ev.Project != "web" || ev.Time.IsZero() {
				t.Fatalf("event = %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("event not delivered")
		}
	}
}

func TestBusSlowOrAbsentSubscriberNeverBlocksPublish(t *testing.T) {
	b := NewBus()
	b.Publish(Event{Message: "nobody listening"})
	slow, unsub := b.Subscribe(1) // never read while publishing
	unbuffered, unsub2 := b.Subscribe(0)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Publish(Event{Message: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// A blocked Publish holds the bus lock, which unsubscribing needs:
		// fail without unsubscribing.
		t.Fatal("Publish blocked on a slow subscriber")
	}
	// Publishing is over, so nothing holds the bus lock any more.
	defer unsub()
	defer unsub2()
	if len(slow) != 1 {
		t.Fatalf("slow subscriber buffered %d events, want its buffer of 1", len(slow))
	}
	select {
	case ev := <-unbuffered:
		t.Fatalf("unbuffered subscriber that was not receiving got %+v", ev)
	default:
	}
}

func TestBusUnsubscribeClosesChannelAndIsIdempotent(t *testing.T) {
	b := NewBus()
	ch, unsub := b.Subscribe(1)
	unsub()
	unsub()
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after unsubscribe")
	}
	b.Publish(Event{Message: "after unsubscribe"}) // must not panic
}

func TestBusSubscribeUnsubscribePublishConcurrently(t *testing.T) {
	b := NewBus()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					b.Publish(Event{Message: "x"})
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				ch, unsub := b.Subscribe(j % 3)
				select {
				case <-ch:
				default:
				}
				unsub()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
