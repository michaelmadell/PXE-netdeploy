// Command agent runs inside WinPE and performs a deployment.
//
// It is launched by winpeshl.ini (written by the boot image builder) instead of
// the default command prompt. Planned sequence, see docs/architecture.md:
//
//  1. wpeinit                            initialise WinPE (PnP, networking stack)
//  2. drvload <staged INFs>              wimlib-built images only
//  3. wpeutil InitializeNetwork; wait    until an IPv4 address is assigned
//  4. POST /agent/check-in               exchange hardware identity for a token
//  5. GET  /agent/deployment             fetch the plan
//  6. partition, apply image, inject drivers, place unattend/identity files
//  7. POST /agent/complete               then reboot
//
// Build for WinPE with: GOOS=windows GOARCH=amd64 (or arm64), see `just build`.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
)

var version = "dev"

// Config is baked into the boot image by the builder (BootImage.serverUrls).
type Config struct {
	ServerURLs []string `json:"serverUrls"`
}

func main() {
	cfgPath := flag.String("config", `X:\netdeploy\agent.json`, "agent config written by the boot image builder")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	slog.Info("netdeploy agent starting", "version", version)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fail(err)
	}
	slog.Info("config loaded", "servers", cfg.ServerURLs)

	// Steps 1-7 land in Phase 4. Until then the agent only proves it starts in WinPE.
	slog.Warn("deployment steps not implemented yet; exiting")
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(c.ServerURLs) == 0 {
		return nil, errors.New("config has no serverUrls")
	}
	return &c, nil
}

// fail logs and exits non-zero. In WinPE, winpeshl.ini then falls back to a
// command prompt so the failure can be inspected on screen.
func fail(err error) {
	slog.Error("agent failed", "err", err)
	os.Exit(1)
}
