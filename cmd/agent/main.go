// Command agent runs inside WinPE and performs a deployment (ADR 0025).
//
// winpeshl.ini (written by the boot image builder) launches only the agent:
//
//  1. drvload the staged drivers (wimlib builds), then wpeinit
//  2. wait for an address and for the disks to settle
//  3. POST /agent/check-in: armed (countdown, then deploy) or locked (the
//     boot prompt: password, profile menu, target disk confirmation)
//  4. fetch the plan, partition, apply the image, add OS drivers,
//     configure, write the boot files
//  5. POST /agent/complete, set the boot order, reboot
//
// A prompt timeout boots the local disk (UEFI BootNext). A failure is
// reported and shown until a key or 10 minutes, then the machine restarts:
// to the prompt, then the disk, never a re-image loop (ADR 0013).
//
// Build for WinPE with `just agent` (dist/) or `just build` (embedded).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agent/client"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/deploy"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/disk"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/firmware"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/sysinfo"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/ui"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/wire"
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

// agent is one run.
type agent struct {
	cfg    Config
	dir    string // X:\netdeploy
	con    *ui.Console
	logs   *logSink
	log    *slog.Logger
	client *client.Client
	disks  []disk.Disk
}

func main() {
	cfgPath := flag.String("config", `X:\netdeploy\config.json`, "agent config written by the boot image builder")
	flag.Parse()

	a := &agent{dir: filepath.Dir(*cfgPath), con: ui.New("netdeploy " + version)}
	a.logs = newLogSink(filepath.Join(a.dir, "agent.log"), a.con)
	a.log = slog.New(a.logs)
	slog.SetDefault(a.log)
	a.con.Screen("Starting")
	a.log.Info("netdeploy agent starting", "version", version)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		a.fatal(err)
	}
	a.cfg = cfg
	if inWinPE() {
		a.startWinPE()
	}
	if err := a.run(context.Background()); err != nil {
		a.fatal(err)
	}
}

func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if len(c.ServerURLs) == 0 {
		return Config{}, errors.New("config has no serverUrls")
	}
	return c, nil
}

// inWinPE reports whether the agent runs in WinPE (the system drive is X:).
func inWinPE() bool { return os.Getenv("SystemDrive") == "X:" }

// startWinPE loads staged drivers (wimlib builds), then initialises WinPE
// (PnP, networking), so NIC and storage drivers are present before
// networking starts and before the disks are listed.
func (a *agent) startWinPE() {
	a.con.Screen("Loading drivers and starting the network")
	for _, d := range a.cfg.Drivers {
		inf := filepath.Join(a.dir, d)
		if out, err := exec.Command("drvload.exe", inf).CombinedOutput(); err != nil {
			a.log.Warn("drvload failed", "inf", inf, "err", err, "output", strings.TrimSpace(string(out)))
		} else {
			a.log.Info("driver loaded", "inf", inf)
		}
	}
	if out, err := exec.Command("wpeinit.exe").CombinedOutput(); err != nil {
		a.log.Warn("wpeinit failed", "err", err, "output", strings.TrimSpace(string(out)))
	}
}

func (a *agent) run(ctx context.Context) error {
	// An address, then the disks (storage drivers expose theirs late).
	for start := time.Now(); len(sysinfo.IPv4()) == 0 && time.Since(start) < 60*time.Second; {
		time.Sleep(time.Second)
	}
	a.log.Info("network", "ipv4", sysinfo.IPv4(), "macs", sysinfo.MACs())
	a.disks = disk.Wait(disk.List, 5*time.Second, 60*time.Second)

	res, err := a.checkIn(ctx)
	if err != nil {
		return err
	}
	switch {
	case res.State == "armed" && res.Armed != nil:
		a.con.Screen("Deployment scheduled: " + res.Armed.ProfileName)
		if !a.con.Countdown("Deploying in", time.Duration(res.Armed.CountdownSeconds)*time.Second+time.Second) {
			return a.deploy(ctx, res.Armed.Token.Token, false)
		}
		// A key: the menu instead (a deployment started there supersedes
		// the armed one).
		return a.prompt(ctx, res, false)
	default:
		return a.prompt(ctx, res, true)
	}
}

// checkIn tries each server URL until one answers (for up to 5 minutes).
func (a *agent) checkIn(ctx context.Context) (wire.CheckInResult, error) {
	id := wire.Identity{MACs: sysinfo.MACs(), AgentVersion: version}
	if s, ok := sysinfo.Read(); ok {
		id.Manufacturer, id.Model, id.Serial = nonEmpty(s.Manufacturer), nonEmpty(s.Model), nonEmpty(s.Serial)
		id.SMBIOSUUID = nonEmpty(s.UUID)
	}
	if len(id.MACs) == 0 {
		return wire.CheckInResult{}, errors.New("no network adapter: the boot image may need this machine's NIC driver")
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		for _, u := range a.cfg.ServerURLs {
			c, err := client.New(u)
			if err != nil {
				a.log.Warn("bad server URL", "url", u, "err", err)
				continue
			}
			c.Retries = 1
			a.con.Screen("Checking in with " + u)
			res, err := c.CheckIn(ctx, id)
			if err == nil {
				c.Retries = 0
				a.client = c
				a.log.Info("checked in", "server", u, "client_id", res.ClientID, "state", res.State)
				return res, nil
			}
			a.log.Warn("check-in failed", "server", u, "err", err)
		}
		if time.Now().After(deadline) {
			return wire.CheckInResult{}, fmt.Errorf("no server answered: %s", strings.Join(a.cfg.ServerURLs, ", "))
		}
		time.Sleep(10 * time.Second)
	}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// prompt is the boot prompt (ADR 0013): password, profile menu, disk
// confirmation. timeoutToDisk: the countdown boots the local disk.
func (a *agent) prompt(ctx context.Context, res wire.CheckInResult, timeoutToDisk bool) error {
	p := res.Prompt
	requirePw, configured, timeout := true, true, 30*time.Second
	if p != nil {
		requirePw, configured, timeout = p.RequirePassword, p.PasswordConfigured, time.Duration(p.TimeoutSeconds)*time.Second
	}
	if !timeoutToDisk {
		timeout = 0
	}
	var unlocked wire.UnlockResult
	for {
		a.con.Screen("This machine is not scheduled for a deployment")
		var pw *string
		switch {
		case !requirePw:
			a.con.Banner("DEBUG MODE: no password is required. Anyone here can re-image this machine.")
			if timeout > 0 && !a.con.Countdown("Press any key for the deployment menu. Booting the local disk in", timeout) {
				return a.bootLocal()
			}
		case !configured:
			a.con.Line("No boot prompt password is set on the server: only deployments scheduled there can run.")
			a.con.Countdown("Booting the local disk in", max(timeout, 10*time.Second))
			return a.bootLocal()
		default:
			a.con.Line("Enter the deployment password to re-image this machine, or wait to boot the local disk.")
			s, ok := a.con.ReadLine("Password: ", true, timeout)
			if !ok {
				return a.bootLocal()
			}
			pw = &s
		}
		u, err := a.client.Unlock(ctx, res.ClientID, pw)
		var ae *client.APIError
		if errors.As(err, &ae) && (ae.Code == "invalid_password" || ae.Code == "too_many_attempts") {
			msg := "Wrong password."
			if ae.RetryAfter > 0 {
				msg = fmt.Sprintf("Too many attempts: wait %s.", ae.RetryAfter)
			}
			a.con.Line(msg)
			a.con.WaitKey(max(ae.RetryAfter, 3*time.Second))
			continue
		}
		if err != nil {
			return fmt.Errorf("unlock: %w", err)
		}
		unlocked = u
		break
	}

	// The profile menu.
	a.con.Screen("Choose what to deploy")
	if len(unlocked.Profiles) == 0 {
		a.con.Line("The server has no profiles.")
		a.con.Countdown("Booting the local disk in", 30*time.Second)
		return a.bootLocal()
	}
	items, def := []string{}, 0
	for i, pr := range unlocked.Profiles {
		label := pr.Name
		if pr.Description != "" {
			label += " - " + pr.Description
		}
		items = append(items, label)
		if unlocked.DefaultProfileID != nil && pr.ID == *unlocked.DefaultProfileID {
			def = i
		}
	}
	items = append(items, "Boot the local disk instead")
	choice, ok := a.con.Menu(items, def)
	if !ok || choice == len(unlocked.Profiles) {
		return a.bootLocal()
	}
	profile := unlocked.Profiles[choice]

	// Confirm the target disk before anything is wiped (auto selection;
	// the plan may name another, checked again in deploy).
	target, err := disk.Select(a.disks, nil)
	a.con.Screen("Deploy " + profile.Name)
	if err != nil {
		a.con.Line("%v", err)
	} else {
		a.con.Line("Everything on %s will be erased.", target)
	}
	if !a.con.Confirm("") {
		return a.bootLocal()
	}
	tok, err := a.client.StartDeployment(ctx, unlocked.SessionToken, profile.ID)
	if err != nil {
		return fmt.Errorf("start the deployment: %w", err)
	}
	return a.deploy(ctx, tok.Token, true)
}

// deploy runs the deployment with its token.
func (a *agent) deploy(ctx context.Context, token string, confirmed bool) error {
	a.client.SetToken(token)
	a.logs.upload(a.client)
	plan, err := a.client.Plan(ctx)
	if err != nil {
		return fmt.Errorf("fetch the plan: %w", err)
	}
	var explicit *int
	if plan.Disk != nil {
		explicit = plan.Disk.TargetDiskIndex
	}
	report := func(stage string, pct int, msg string) {
		a.con.Progress("Deploying", stage, pct, msg)
		if err := a.client.Progress(ctx, wire.Progress{Stage: stage, Percent: pct, Message: msg}); err != nil {
			a.log.Warn("progress report failed", "err", err)
		}
	}
	report(wire.StageCheckedIn, 0, "Deployment "+plan.DeploymentID)
	target, err := disk.Select(a.disks, explicit)
	if err == nil {
		a.log.Info("target disk", "disk", target.String(), "explicit", explicit != nil)
		env := deploy.Env{Client: a.client, Run: deploy.Exec{Log: a.log}, Log: a.log, Report: report, Work: a.dir}
		err = deploy.Install(ctx, env, plan, target)
	}
	if err != nil {
		a.log.Error("deployment failed", "err", err)
		if cerr := a.client.Complete(ctx, false, err.Error()); cerr != nil {
			a.log.Warn("report the failure", "err", cerr)
		}
		a.logs.flush()
		return err
	}

	// Success: report first, then the boot order, then reboot (ADR 0013).
	a.log.Info("deployment succeeded", "deployment_id", plan.DeploymentID)
	if err := deploy.CopyFile(filepath.Join(a.dir, "agent.log"), `W:\Windows\Temp\netdeploy-agent.log`); err != nil {
		a.log.Warn("copy the agent log to the OS", "err", err)
	}
	report(wire.StageRebooting, 100, "Done")
	a.logs.flush()
	if err := a.client.Complete(ctx, true, ""); err != nil {
		a.log.Warn("report completion", "err", err)
	}
	if plan.BootOrderAfterDeploy == "network-first" {
		if v, err := firmware.Open(); err != nil {
			a.log.Warn("boot order", "err", err)
		} else if _, err := firmware.NetworkFirst(v); err != nil {
			a.log.Warn("network-first boot order", "err", err)
		}
	}
	a.logs.flush()
	a.con.Screen("Deployment finished")
	a.con.Line("Restarting into Windows Setup...")
	time.Sleep(5 * time.Second)
	reboot()
	return nil
}

// bootLocal sets BootNext to the local disk's boot manager and reboots, or
// powers off when the disk has none (no loop back into the prompt).
func (a *agent) bootLocal() error {
	a.con.Screen("Booting the local disk")
	v, err := firmware.Open()
	if err == nil {
		var entries []firmware.Entry
		if entries, err = firmware.Entries(v); err == nil {
			if e, ok := firmware.LocalDisk(entries); ok {
				if err = firmware.SetBootNext(v, e.Number); err == nil {
					a.log.Info("booting the local disk", "entry", e.String())
					time.Sleep(time.Second)
					reboot()
					return nil
				}
			} else {
				err = errors.New("no boot entry on a local disk")
			}
		}
	}
	a.log.Warn("can't boot the local disk", "err", err)
	a.con.Line("This machine has no operating system to boot (%v).", err)
	a.con.Countdown("Shutting down in", 60*time.Second)
	exec.Command("wpeutil.exe", "shutdown").Run()
	return nil
}

func reboot() { exec.Command("wpeutil.exe", "reboot").Run() }

// fatal shows the error until a key or 10 minutes, then restarts (to the
// prompt, then the disk: no loop).
func (a *agent) fatal(err error) {
	a.log.Error("agent failed", "err", err)
	a.logs.flush()
	a.con.Screen("Deployment failed")
	a.con.Banner(err.Error())
	a.con.Line("The log is in %s.", filepath.Join(a.dir, "agent.log"))
	if !inWinPE() {
		os.Exit(1)
	}
	a.con.Line("Press any key to restart (it restarts by itself in 10 minutes).")
	a.con.WaitKey(10 * time.Minute)
	reboot()
	os.Exit(1)
}
