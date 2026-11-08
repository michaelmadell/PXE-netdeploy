package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agent/client"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/ui"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/wire"
)

// logSink is the agent's slog handler: each record goes to agent.log, the
// console's log tail, and (once a deployment has a token) the server, in
// batches every 5 s.
type logSink struct {
	mu      sync.Mutex
	file    *os.File
	con     *ui.Console
	pending []wire.LogEntry
	client  *client.Client
	attrs   []slog.Attr
}

func newLogSink(path string, con *ui.Console) *logSink {
	f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	return &logSink{file: f, con: con}
}

func (l *logSink) Enabled(_ context.Context, lvl slog.Level) bool { return lvl >= slog.LevelDebug }

func (l *logSink) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	add := func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
		return true
	}
	for _, a := range l.attrs {
		add(a)
	}
	r.Attrs(add)
	msg := sb.String()
	level := strings.ToLower(r.Level.String())
	line := fmt.Sprintf("%s %-5s %s", r.Time.Format("15:04:05"), strings.ToUpper(level), msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		fmt.Fprintln(l.file, line)
	}
	if r.Level >= slog.LevelInfo && l.con != nil {
		l.con.Log(line)
	}
	if len(l.pending) < 10000 {
		l.pending = append(l.pending, wire.LogEntry{At: r.Time.UTC(), Level: level, Message: truncate(msg, 8192), Source: "agent"})
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (l *logSink) WithAttrs(as []slog.Attr) slog.Handler {
	return &childSink{parent: l, attrs: append([]slog.Attr{}, as...)}
}

func (l *logSink) WithGroup(string) slog.Handler { return l }

// childSink carries attributes and forwards to its parent.
type childSink struct {
	parent *logSink
	attrs  []slog.Attr
}

func (c *childSink) Enabled(ctx context.Context, lvl slog.Level) bool {
	return c.parent.Enabled(ctx, lvl)
}
func (c *childSink) Handle(ctx context.Context, r slog.Record) error {
	r.AddAttrs(c.attrs...)
	return c.parent.Handle(ctx, r)
}
func (c *childSink) WithAttrs(as []slog.Attr) slog.Handler {
	return &childSink{parent: c.parent, attrs: append(append([]slog.Attr{}, c.attrs...), as...)}
}
func (c *childSink) WithGroup(string) slog.Handler { return c }

// upload starts sending the queue to the server every 5 s.
func (l *logSink) upload(c *client.Client) {
	l.mu.Lock()
	start := l.client == nil
	l.client = c
	l.mu.Unlock()
	if !start {
		return
	}
	go func() {
		for range time.Tick(5 * time.Second) {
			l.flush()
		}
	}()
}

// flush sends what is queued (up to 1000 entries per call); entries stay
// queued when the server can't be reached.
func (l *logSink) flush() {
	l.mu.Lock()
	c := l.client
	batch := append([]wire.LogEntry{}, l.pending[:min(len(l.pending), 1000)]...)
	l.mu.Unlock()
	if c == nil || len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Logs(ctx, batch); err != nil {
		return
	}
	l.mu.Lock()
	l.pending = l.pending[len(batch):]
	l.mu.Unlock()
}
