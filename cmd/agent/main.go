// Command agent runs inside WinPE and performs a deployment.
//
// It is launched by winpeshl.ini (written by the boot image builder) instead of
// the default command prompt. Planned sequence, see docs/architecture.md:
//
//  1. drvload <staged INFs>              wimlib-built images only
//  2. wpeinit                            initialise WinPE (PnP, networking stack)
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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var version = "dev"

// Config is X:\netdeploy\config.json, written by the boot image builder
// (internal/bootimage.AgentConfig, ADR 0022).
type Config struct {
	ServerURLs []string `json:"serverUrls"`
	InputHash  string   `json:"inputHash"`
	// Drivers are INF paths relative to the config's directory, staged by
	// wimlib builds and loaded with drvload before wpeinit.
	Drivers      []string `json:"drivers"`
	ContentCAPEM string   `json:"contentCaPem"`
}

func main() {
	cfgPath := flag.String("config", `X:\netdeploy\config.json`, "agent config written by the boot image builder")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	slog.Info("netdeploy agent starting", "version", version)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fail(err)
	}
	slog.Info("config loaded", "servers", cfg.ServerURLs, "build", cfg.InputHash)
	if inWinPE() {
		startWinPE(filepath.Dir(*cfgPath), cfg.Drivers)
	}

	// Steps 3-7 land with Phase 4 decision 4. Until then the agent proves
	// it starts in WinPE: it shows the addresses wpeinit brought up, then
	// waits so the screen can be read (when it exits, WinPE restarts).
	logAddresses()
	if inWinPE() {
		// Storage drivers loaded above expose their disks a little later.
		waitForDisks(5*time.Second, 60*time.Second)
		slog.Warn("deployment steps not implemented yet; restarting in 2 minutes")
		time.Sleep(2 * time.Minute)
		return
	}
	slog.Warn("deployment steps not implemented yet; exiting")
}

// logAddresses logs each up interface's IPv4 addresses.
func logAddresses() {
	ifaces, err := net.Interfaces()
	if err != nil {
		slog.Warn("list interfaces", "err", err)
		return
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		var v4 []string
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				v4 = append(v4, n.String())
			}
		}
		slog.Info("interface", "name", ifc.Name, "mac", ifc.HardwareAddr.String(), "ipv4", v4)
	}
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

// fail logs and exits non-zero. winpeshl.ini launches only the agent, so in
// WinPE the machine then restarts (the boot prompt UI is Phase 4 decision 4).
func fail(err error) {
	slog.Error("agent failed", "err", err)
	os.Exit(1)
}

// inWinPE reports whether the agent runs in WinPE (the system drive is X:).
func inWinPE() bool { return os.Getenv("SystemDrive") == "X:" }

// startWinPE loads staged drivers (wimlib builds), then initialises WinPE
// (PnP, networking), so a NIC driver is present before networking starts.
// winpeshl.ini launches only the agent, so this replaces its usual wpeinit.
func startWinPE(dir string, drivers []string) {
	for _, d := range drivers {
		inf := filepath.Join(dir, d)
		if out, err := exec.Command("drvload.exe", inf).CombinedOutput(); err != nil {
			slog.Warn("drvload failed", "inf", inf, "err", err, "output", string(out))
		} else {
			slog.Info("driver loaded", "inf", inf)
		}
	}
	if out, err := exec.Command("wpeinit.exe").CombinedOutput(); err != nil {
		slog.Warn("wpeinit failed", "err", err, "output", string(out))
	}
}
