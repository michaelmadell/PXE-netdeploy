package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// serviceName is the Windows service's name (ADR 0031).
const serviceName = "netdeploy"

// runService runs the server under the Service Control Manager when it
// started us; otherwise it reports false and main runs it directly.
func runService(o options) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, nil
	}
	return true, svc.Run(serviceName, &service{o: o})
}

type service struct{ o options }

func (s *service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, s.o) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			// The server stopped on its own: report failure so the
			// recovery actions restart it.
			if err != nil {
				slog.Error("fatal", "err", err)
			}
			return true, 1
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 30_000}
				cancel()
				if err := <-done; err != nil {
					slog.Error("stopped with an error", "err", err)
				}
				return false, 0
			}
		}
	}
}

const serviceUsage = `usage (elevated):
  netdeployd service install -data <dir> [-content <dir>]   register (or update) the service; automatic start
  netdeployd service uninstall                              stop and remove it
  netdeployd service start | stop`

// serviceCommand registers and controls the Windows service. The installer
// uses it, so scripted installs get the same service (ADR 0031).
func serviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(serviceUsage)
	}
	switch args[0] {
	case "install":
		return installService(args[1:])
	case "uninstall":
		return uninstallService()
	case "start":
		return withService(func(s *mgr.Service) error { return startAndWait(s) })
	case "stop":
		return withService(func(s *mgr.Service) error { return stopAndWait(s) })
	}
	return errors.New(serviceUsage)
}

func installService(args []string) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	dataDir := fs.String("data", `C:\ProgramData\netdeploy`, "data directory")
	contentDir := fs.String("content", "", "content store directory; default <data>\\content")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	svcArgs := []string{"-data", data, "-log-file", filepath.Join(data, "logs", "netdeployd.log")}
	if *contentDir != "" {
		c, err := filepath.Abs(*contentDir)
		if err != nil {
			return err
		}
		svcArgs = append(svcArgs, "-content", c)
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w (run elevated)", err)
	}
	defer m.Disconnect()

	cfg := mgr.Config{
		DisplayName: "netdeploy",
		Description: "Network deployment server for Windows (PXE, TFTP, HTTP(S), the WinPE agent).",
		StartType:   mgr.StartAutomatic,
		// Empty ServiceStartName: LocalSystem (ADR 0031).
	}
	s, err := m.OpenService(serviceName)
	if err == nil {
		// An upgrade or a new data directory: same service, new command line.
		cfg.BinaryPathName = commandLine(exe, svcArgs)
		if err := s.UpdateConfig(cfg); err != nil {
			s.Close()
			return fmt.Errorf("update service: %w", err)
		}
		fmt.Println("Updated the netdeploy service.")
	} else {
		if s, err = m.CreateService(serviceName, exe, cfg, svcArgs...); err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		fmt.Println("Registered the netdeploy service (automatic start, LocalSystem).")
	}
	defer s.Close()
	// Restart after 5 s, 30 s, then every minute; the count resets after a day.
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}
	if err := s.SetRecoveryActions(actions, 24*60*60); err != nil {
		return fmt.Errorf("service recovery: %w", err)
	}
	// Also restart when the server exits with an error, not only on a crash.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("service recovery: %w", err)
	}
	return nil
}

func uninstallService() error {
	return withService(func(s *mgr.Service) error {
		if err := stopAndWait(s); err != nil {
			return err
		}
		if err := s.Delete(); err != nil {
			return fmt.Errorf("delete service: %w", err)
		}
		fmt.Println("Removed the netdeploy service.")
		return nil
	})
}

func withService(f func(*mgr.Service) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w (run elevated)", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("the netdeploy service isn't installed: %w", err)
	}
	defer s.Close()
	return f(s)
}

func startAndWait(s *mgr.Service) error {
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("start service: %w", err)
	}
	return waitFor(s, svc.Running)
}

func stopAndWait(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return fmt.Errorf("stop service: %w", err)
	}
	return waitFor(s, svc.Stopped)
}

func waitFor(s *mgr.Service, want svc.State) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if want == svc.Running && st.State == svc.Stopped {
			return errors.New("the service stopped during start: see the log in <data>\\logs\\netdeployd.log")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the service didn't reach state %d within a minute", want)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// commandLine quotes the program and arguments as the service manager wants.
func commandLine(exe string, args []string) string {
	parts := []string{windows.EscapeArg(exe)}
	for _, a := range args {
		parts = append(parts, windows.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}
