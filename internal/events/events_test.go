package events

import "testing"

func TestFanOut(t *testing.T) {
	b := New()
	a, c := b.Subscribe(), b.Subscribe()
	defer a.Close()
	defer c.Close()
	b.Publish(Event{Type: ClientUpdated, ResourceID: "x"})
	for _, s := range []*Subscription{a, c} {
		e := <-s.C
		if e.Type != ClientUpdated || e.ResourceID != "x" || e.At.IsZero() {
			t.Errorf("got %+v", e)
		}
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	b := New()
	slow, fast := b.Subscribe(), b.Subscribe()
	defer fast.Close()
	for range SubscriberBuffer + 1 {
		b.Publish(Event{Type: LeaseUpdated})
		<-fast.C // fast keeps up
	}
	n := 0
	for range slow.C {
		n++
	}
	if n != SubscriberBuffer || !slow.Dropped() {
		t.Fatalf("slow got %d events, dropped=%v; want %d and dropped", n, slow.Dropped(), SubscriberBuffer)
	}
	if b.Subscribers() != 1 {
		t.Errorf("subscribers = %d, want 1", b.Subscribers())
	}
	slow.Close() // already closed by the bus: must not panic
}

func TestNilBusDiscards(t *testing.T) {
	var b *Bus
	b.Publish(Event{Type: HealthUpdated}) // must not panic
}
