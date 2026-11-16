package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// SweepDeployments applies the deployment timeouts (ADR 0026): armed
// deployments not picked up in time fail `expired`, running ones the agent
// has gone quiet on fail `timeout`.
func (s *Server) SweepDeployments(ctx context.Context) error {
	lim := settings.Defaults().Deployments
	if s.settings != nil {
		cfg, _ := s.settings.Current()
		lim = cfg.Deployments
	}
	now := s.agent.clock()
	expired, idle, err := s.store.DueDeployments(ctx,
		now.Add(-time.Duration(lim.ArmedExpiryHours)*time.Hour), now.Add(-time.Duration(lim.InactivityMinutes)*time.Minute))
	if err != nil {
		return err
	}
	fail := func(id, msg string) {
		d, err := s.store.FinishDeployment(ctx, id, store.StatusFailed, msg)
		if err != nil {
			slog.Warn("deployment timeout", "deployment_id", id, "err", err)
			return
		}
		slog.Warn("deployment failed", "deployment_id", id, "client_id", d.ClientID, "reason", msg)
		s.deploymentEvent(d)
	}
	for _, id := range expired {
		fail(id, "expired: not picked up within "+time.Duration(lim.ArmedExpiryHours*int(time.Hour)).String())
	}
	for _, id := range idle {
		fail(id, "timeout: no word from the agent for "+time.Duration(lim.InactivityMinutes*int(time.Minute)).String())
	}
	return nil
}

// RunDeploymentSweeper runs SweepDeployments every minute until ctx ends.
func (s *Server) RunDeploymentSweeper(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.SweepDeployments(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("deployment sweep", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
