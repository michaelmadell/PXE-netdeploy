package server

import (
	"context"
	"errors"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Ending sessions at once (ADR 0029): when sessions or tokens end, an
// events.SessionEnded event on the bus tells each event stream whether it
// belonged to one of them. Matching streams send `session.ended` to their
// browser, which shows the sign-in page, and close. Other streams never see
// the event.

// Reasons, as sent to the browser.
const (
	endPasswordChanged = "password_changed"
	endAccountDeleted  = "account_deleted"
	endSignedOut       = "signed_out"
	endTokenRevoked    = "token_revoked"
	endExpired         = "expired"
)

// sessionEnd is the bus event's data, never sent to a client as it is.
type sessionEnd struct {
	Reason  string
	Session string // one session (its hash)
	Token   string // one token (its id)
	UserID  string // every session of this account...
	Keep    string // ...except this one
	Tokens  bool   // ...and its tokens too
}

func (e sessionEnd) matches(p Principal) bool {
	switch {
	case e.Session != "":
		return p.Via == "cookie" && p.SessionHash == e.Session
	case e.Token != "":
		return p.Via == "token" && p.TokenID == e.Token
	case e.UserID != "" && p.UserID == e.UserID:
		return (p.Via == "cookie" && p.SessionHash != e.Keep) || (p.Via == "token" && e.Tokens)
	}
	return false
}

func (s *Server) endSessions(e sessionEnd) {
	s.events.Publish(events.Event{Type: events.SessionEnded, Data: e})
}

// stillValid re-checks a stream's cookie session without counting as
// activity (an open stream mustn't keep an idle session alive).
func (s *Server) stillValid(ctx context.Context, p Principal) bool {
	if p.Via != "cookie" || s.store == nil {
		return true
	}
	x, err := s.store.AdminSessionByHash(ctx, p.SessionHash)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		return true // a database hiccup isn't a reason to sign anyone out
	}
	now := s.auth.clock()
	return now.Before(x.ExpiresAt) && now.Sub(x.LastSeenAt) < sessionIdle
}

// sessionEndedEvent is what the browser gets.
func sessionEndedEvent(reason string, at time.Time) events.Event {
	return events.Event{Type: events.SessionEnded, At: at, Data: map[string]string{"reason": reason}}
}
