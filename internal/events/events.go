// Package events is an in-memory publish/subscribe bus for GET /events
// (server-sent events). Nothing is stored: events tell a client to re-fetch
// something, they are not a log. A publisher never waits for a subscriber;
// a subscriber that falls behind is disconnected and should reconnect and
// re-read what it shows.
package events

import (
	"sync"
	"time"
)

// Event types (api Event.type).
const (
	ClientUpdated    = "client.updated"
	LeaseUpdated     = "lease.updated"
	SettingsUpdated  = "settings.updated"
	HealthUpdated    = "health.updated"
	JobUpdated       = "job.updated"
	ImageUpdated     = "image.updated"
	DriverUpdated    = "driverPackage.updated"
	BootImageUpdated = "bootImage.updated"
)

// Event is one notification. Data is marshalled as the Event's `data`.
type Event struct {
	Type       string    `json:"type"`
	At         time.Time `json:"at"`
	ResourceID string    `json:"resourceId,omitempty"`
	Data       any       `json:"data,omitempty"`
}

// SubscriberBuffer is how many events a subscriber may fall behind before it
// is disconnected.
const SubscriberBuffer = 64

// Bus fans events out to subscribers. The zero value is not usable; use New.
// A nil *Bus discards everything, so publishers needn't check.
type Bus struct {
	mu   sync.Mutex
	subs map[*Subscription]struct{}
	now  func() time.Time
}

// New returns an empty Bus.
func New() *Bus {
	return &Bus{subs: map[*Subscription]struct{}{}, now: time.Now}
}

// Subscription receives events on C until it is closed: by Close, or by the
// bus when the subscriber fell behind (Dropped then reports true).
type Subscription struct {
	C       <-chan Event
	c       chan Event
	bus     *Bus
	once    sync.Once
	dropped bool
}

// Subscribe registers a new subscriber.
func (b *Bus) Subscribe() *Subscription {
	c := make(chan Event, SubscriberBuffer)
	s := &Subscription{C: c, c: c, bus: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Close unsubscribes. Safe to call more than once.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	s.closeLocked()
}

func (s *Subscription) closeLocked() {
	s.once.Do(func() {
		delete(s.bus.subs, s)
		close(s.c)
	})
}

// Dropped reports whether the bus disconnected s for falling behind. Read it
// after C is closed.
func (s *Subscription) Dropped() bool {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return s.dropped
}

// Publish sends an event to every subscriber without blocking. At defaults
// to now.
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if e.At.IsZero() {
		e.At = b.now().UTC()
	}
	for s := range b.subs {
		select {
		case s.c <- e:
		default:
			s.dropped = true
			s.closeLocked()
		}
	}
}

// Subscribers is the current number of subscribers (tests, health).
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
