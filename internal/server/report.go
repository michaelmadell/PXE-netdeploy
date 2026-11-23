package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Deployment status without an agent (ADR 0036): the Ubuntu installer
// reports to /content/report/<deployment token>/: Subiquity's reporting
// webhook (events), and curl hooks from the template (started, finished,
// failed). Milestones in the events move the deployment's stage and percent.

// ReportPrefix is where content listeners take installer reports.
const ReportPrefix = "/content/report/"

// maxReportBody bounds one report.
const maxReportBody = 64 << 10

// reportTouchEvery throttles the inactivity timestamp under a stream of events.
const reportTouchEvery = 30 * time.Second

// installerEvent is one Subiquity/curtin reporting event.
type installerEvent struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	EventType   string  `json:"event_type"` // start, finish
	Result      string  `json:"result"`     // SUCCESS, WARN, FAIL (finish)
	Level       string  `json:"level"`      // DEBUG, INFO, WARNING, ERROR
	Origin      string  `json:"origin"`
	Timestamp   float64 `json:"timestamp"`
}

// milestone is an installer step that moves the deployment on.
type milestone struct {
	match   string // a suffix, or a /segment/ inside, of the event name
	stage   string
	percent int
	message string
}

// milestones, in order. Names from the Phase 7 spike's webhook log
// (Subiquity 26.04, curtin 24): the start of each step.
var milestones = []milestone{
	{"subiquity/apply_autoinstall_config", "checked-in", 10, "applying the autoinstall settings"},
	{"/install/configure_apt", "checked-in", 15, "configuring apt"},
	{"/stage-partitioning", "partitioning", 20, "partitioning the disk"},
	{"/stage-extract", "applying-image", 30, "writing the system to disk"},
	{"/stage-curthooks", "configuring", 55, "configuring the installed system"},
	{"/installing-missing-packages", "configuring", 60, "installing boot packages"},
	{"/installing-kernel", "configuring", 70, "installing the kernel"},
	{"/system-upgrade", "configuring", 80, "updating packages"},
	{"/install/postinstall", "finalizing", 90, "final system configuration"},
	{"subiquity/Late/run", "finalizing", 95, "running late-commands"},
}

func eventMilestone(name string) (milestone, bool) {
	for i := len(milestones) - 1; i >= 0; i-- {
		m := milestones[i]
		if strings.HasSuffix(name, m.match) || strings.Contains(name, m.match+"/") {
			return m, true
		}
	}
	return milestone{}, false
}

// reportProgress moves a running deployment on (never back) and publishes
// it. Errors are logged: a report must never fail the install.
func (s *Server) reportProgress(ctx context.Context, id, stage string, percent int, message string) {
	d, err := s.store.Deployment(ctx, id)
	if err != nil || d.Status != store.StatusRunning || d.Percent >= percent {
		return
	}
	if err := s.store.ReportDeploymentProgress(ctx, id, stage, percent, message); err != nil {
		if !errors.Is(err, store.ErrBadTransition) {
			slog.Warn("installer progress", "svc", "agent", "deployment_id", id, "err", err)
		}
		return
	}
	if d, err := s.store.Deployment(ctx, id); err == nil {
		s.deploymentEvent(d)
	}
}

// touch records installer activity at most every reportTouchEvery.
func (s *Server) touch(ctx context.Context, id string) {
	a := &s.agent
	a.mu.Lock()
	if a.touched == nil {
		a.touched = map[string]time.Time{}
	}
	now := a.clock()
	due := now.Sub(a.touched[id]) >= reportTouchEvery
	if due {
		a.touched[id] = now
	}
	a.mu.Unlock()
	if due {
		if err := s.store.TouchDeployment(ctx, id); err != nil {
			slog.Warn("record installer activity", "svc", "agent", "deployment_id", id, "err", err)
		}
	}
}

// finishInstall ends a running Linux deployment from a hook or event.
func (s *Server) finishInstall(ctx context.Context, id, status, message string) {
	d, err := s.store.FinishDeployment(ctx, id, status, message)
	if errors.Is(err, store.ErrBadTransition) {
		return // already finished: a repeated hook
	}
	if err != nil {
		slog.Warn("finish installer deployment", "svc", "agent", "deployment_id", id, "err", err)
		return
	}
	slog.Info("deployment finished", "svc", "agent", "via", "installer", "deployment_id", d.ID, "status", d.Status, "message", d.Message)
	s.deploymentEvent(d)
}

func (s *Server) installerLog(ctx context.Context, id, level, message string) {
	src := "installer"
	if err := s.store.AppendDeploymentLogs(ctx, id, []store.LogEntry{{At: time.Now(), Level: level, Message: message, Source: &src}}); err != nil {
		slog.Warn("installer log", "svc", "agent", "deployment_id", id, "err", err)
	}
}

// LinuxReport takes an installer report for a running deployment: kind is
// events (a webhook event), started, finished or failed. status is the HTTP
// status; quiet asks the content listener not to log a success at info
// level (the webhook posts hundreds of events).
func (s *Server) LinuxReport(r *http.Request, token, kind string) (status int, quiet bool, attrs []any) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		return http.StatusMethodNotAllowed, false, nil
	}
	t, err := s.store.DeploymentToken(ctx, tokenHash(token), 0)
	if errors.Is(err, store.ErrTokenInvalid) {
		return http.StatusNotFound, false, []any{"reason", "unknown token, or the deployment isn't running"}
	}
	if err != nil {
		return http.StatusInternalServerError, false, []any{"err", err.Error()}
	}
	id := *t.DeploymentID
	attrs = []any{"deployment_id", id, "report", kind}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBody+1))
	if err != nil || len(body) > maxReportBody {
		return http.StatusRequestEntityTooLarge, false, attrs
	}
	s.touch(ctx, id)
	switch kind {
	case "started":
		s.installerLog(ctx, id, "info", "The installer started (early-commands).")
		s.reportProgress(ctx, id, "checked-in", 8, "the installer started")
	case "finished":
		s.installerLog(ctx, id, "info", "The install finished (late-commands); the machine reboots into the installed system.")
		s.finishInstall(ctx, id, store.StatusSucceeded, "installed; rebooting into the installed system")
	case "failed":
		msg := "The installer failed (error-commands)."
		if text := strings.TrimSpace(string(body)); text != "" {
			msg += " " + firstLine(text, 300)
		}
		s.installerLog(ctx, id, "error", msg)
		s.finishInstall(ctx, id, store.StatusFailed, "the installer failed; see its logs on the machine (/var/log/installer)")
	case "events":
		var e installerEvent
		if err := json.Unmarshal(body, &e); err != nil {
			return http.StatusBadRequest, false, append(attrs, "err", "not an installer event")
		}
		s.installerEvent(ctx, id, e)
		return http.StatusNoContent, true, attrs
	default:
		return http.StatusNotFound, false, append(attrs, "reason", "no such report")
	}
	return http.StatusNoContent, false, attrs
}

// installerEvent handles one webhook event: milestones move the deployment
// on; INFO and louder events, and failed steps, go to its log; a failed
// install fails it.
func (s *Server) installerEvent(ctx context.Context, id string, e installerEvent) {
	if e.EventType == "start" {
		if m, ok := eventMilestone(e.Name); ok {
			s.reportProgress(ctx, id, m.stage, m.percent, m.message)
		}
	}
	failed := e.EventType == "finish" && e.Result == "FAIL"
	level := map[string]string{"INFO": "info", "WARNING": "warn", "ERROR": "error"}[e.Level]
	if failed {
		level = "error"
	}
	if level != "" && !strings.Contains(e.Name, "/Meta/") {
		msg := e.EventType + " " + e.Name
		if e.Description != "" {
			msg += ": " + firstLine(e.Description, 300)
		}
		if e.Result != "" && e.Result != "SUCCESS" {
			msg += " (" + e.Result + ")"
		}
		s.installerLog(ctx, id, level, msg)
	}
	if failed && (e.Name == "subiquity/Install/install" || strings.HasPrefix(e.Name, "subiquity/Install/install/")) {
		s.finishInstall(ctx, id, store.StatusFailed, fmt.Sprintf("the installer failed at %s", strings.TrimPrefix(e.Name, "subiquity/")))
	}
}

func firstLine(s string, max int) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
