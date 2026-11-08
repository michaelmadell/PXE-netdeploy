package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/backup"
)

func backupNotConfigured() api.Problem {
	return newProblem(http.StatusConflict, "backup_not_configured", "Backups not configured",
		"Set backup.destination in the settings first.", nil)
}

// ListBackups implements GET /system/backups.
func (s *Server) ListBackups(ctx context.Context, _ api.ListBackupsRequestObject) (api.ListBackupsResponseObject, error) {
	if s.backup == nil {
		return api.ListBackups409ApplicationProblemPlusJSONResponse(backupNotConfigured()), nil
	}
	list, err := s.backup.List(ctx)
	switch {
	case errors.Is(err, backup.ErrNotConfigured):
		return api.ListBackups409ApplicationProblemPlusJSONResponse(backupNotConfigured()), nil
	case err != nil:
		return api.ListBackups502ApplicationProblemPlusJSONResponse(newProblem(http.StatusBadGateway, "backup_destination_error",
			"Backup destination error", err.Error(), nil)), nil
	}
	out := api.ListBackups200JSONResponse{Items: make([]api.Backup, len(list))}
	for i, m := range list {
		b := api.Backup{Id: m.ID, CreatedAt: m.CreatedAt, Trigger: api.BackupTrigger(m.Trigger), ServerVersion: m.ServerVersion,
			SchemaVersion: m.SchemaVersion, BlobCount: m.BlobCount, BlobBytes: m.BlobBytes, DatabaseBytes: m.DatabaseBytes}
		if m.SecretsKeyFingerprint != "" {
			fp := m.SecretsKeyFingerprint
			b.SecretsKeyFingerprint = &fp
		}
		out.Items[i] = b
	}
	return out, nil
}

// RunBackup implements POST /system/backups.
func (s *Server) RunBackup(ctx context.Context, _ api.RunBackupRequestObject) (api.RunBackupResponseObject, error) {
	if s.backup == nil {
		return api.RunBackup409ApplicationProblemPlusJSONResponse(backupNotConfigured()), nil
	}
	j, err := s.backup.Enqueue(ctx, backup.TriggerManual)
	if errors.Is(err, backup.ErrNotConfigured) {
		return api.RunBackup409ApplicationProblemPlusJSONResponse(backupNotConfigured()), nil
	}
	if err != nil {
		return nil, err
	}
	s.jobEvent(j)
	return api.RunBackup202JSONResponse{JobAcceptedJSONResponse: jobAccepted(j).JobAcceptedJSONResponse}, nil
}

// backupHealth fills Health.backup and adds backup warnings.
func (s *Server) backupHealth(ctx context.Context) (*api.BackupHealth, []warning) {
	if s.backup == nil {
		return nil, nil
	}
	h := s.backup.Health(ctx)
	out := &api.BackupHealth{LastSuccessAt: h.LastSuccessAt, LastFailureAt: h.LastFailureAt, NextRunAt: h.NextRunAt}
	if h.LastError != "" && h.LastFailureAt != nil && (h.LastSuccessAt == nil || h.LastFailureAt.After(*h.LastSuccessAt)) {
		e := h.LastError
		out.LastError = &e
	}
	var w []warning
	for _, x := range h.Warnings {
		w = append(w, warning{x[0], x[1]})
	}
	return out, w
}
