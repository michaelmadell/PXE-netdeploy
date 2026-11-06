package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
)

// heartbeat keeps proxies and browsers from closing an idle stream.
var heartbeat = 15 * time.Second

// StreamEvents implements GET /events as server-sent events.
func (s *Server) StreamEvents(ctx context.Context, _ api.StreamEventsRequestObject) (api.StreamEventsResponseObject, error) {
	return eventStream{ctx: ctx, bus: s.events}, nil
}

// eventStream writes the stream when the generated handler visits it, and
// returns when the client goes away or the bus drops it for falling behind.
type eventStream struct {
	ctx context.Context
	bus *events.Bus
}

func (e eventStream) VisitStreamEventsResponse(w http.ResponseWriter) error {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if e.bus == nil {
		return nil
	}
	sub := e.bus.Subscribe()
	defer sub.Close()
	// Tell the client the stream is live before the first event arrives.
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return nil
	}
	if err := rc.Flush(); err != nil {
		return err
	}

	t := time.NewTicker(heartbeat)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return nil
		case <-t.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return nil
			}
		case ev, ok := <-sub.C:
			if !ok {
				if sub.Dropped() {
					slog.Info("events subscriber dropped for falling behind")
				}
				return nil
			}
			data, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
				return nil
			}
		}
		if err := rc.Flush(); err != nil {
			return nil
		}
	}
}
