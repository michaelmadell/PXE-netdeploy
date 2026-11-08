package bootimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CleanupMounts discards DISM mounts that builds left under the content
// store's staging directory when the server stopped (ADR 0022). Call it
// before cas.Open, which empties staging. Unelevated it can only report
// them. It returns the mount directories it couldn't discard.
func CleanupMounts(ctx context.Context, contentRoot string, log *slog.Logger) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	mounts, _ := filepath.Glob(filepath.Join(contentRoot, "staging", "*", "work", "mount"))
	if len(mounts) == 0 {
		return nil
	}
	h := Detect(ctx, "")
	if !h.DISM.Available {
		log.Error("DISM mounts left by an interrupted build can't be discarded: "+h.DISM.Reason, "mounts", mounts)
		return mounts
	}
	d := dismTool{path: h.DISM.Path}
	var left []string
	for _, m := range mounts {
		logPath := filepath.Join(filepath.Dir(m), "dism-cleanup.log")
		if err := d.run(logPath, "/Unmount-Image", "/MountDir:"+m, "/Discard"); err != nil {
			log.Warn("discard DISM mount", "mount", m, "err", err)
		} else {
			log.Info("discarded DISM mount left by an interrupted build", "mount", m)
		}
	}
	if err := d.run(filepath.Join(os.TempDir(), "netdeploy-dism-cleanup.log"), "/Cleanup-Mountpoints"); err != nil {
		log.Warn("dism /Cleanup-Mountpoints", "err", err)
	}
	for _, m := range mounts {
		if ents, err := os.ReadDir(m); err == nil && len(ents) > 0 {
			left = append(left, m)
		}
	}
	return left
}
