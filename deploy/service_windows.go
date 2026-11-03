//go:build windows

package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// setWindowsDefaults populates zero-value fields with Windows service defaults.
func (c *ServiceConfig) setWindowsDefaults() {
	if c.ServiceName == "" {
		c.ServiceName = binaryName
	}
	if c.ServiceUser == "" {
		c.ServiceUser = "LocalSystem"
	}
	if c.Port == 0 {
		c.Port = 8000
	}
	if c.Interval == 0 {
		c.Interval = time.Minute
	}
}

// resolveWindowsExecPath returns the executable path to register with SCM.
func resolveWindowsExecPath(configured string) string {
	if configured != "" {
		return configured
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
		return exe
	}
	return binaryName + ".exe"
}

// windowsServiceArgs returns the CLI arguments passed when starting the service.
func windowsServiceArgs(cfg ServiceConfig) []string {
	args := []string{
		fmt.Sprintf("--port=%d", cfg.Port),
		fmt.Sprintf("--interval=%s", cfg.Interval),
	}
	if cfg.ConfigPath != "" {
		args = append(args, fmt.Sprintf("--config=%s", cfg.ConfigPath))
	}
	return args
}

// IsWindowsService reports whether the current process is running as a Windows Service under SCM.
func IsWindowsService() bool {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return isSvc
}

type winServiceHandler struct {
	runFn func(ctx context.Context) error
	err   error
}

func (h *winServiceHandler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.runFn(ctx)
	}()

	s <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				err := <-errCh
				if err != nil && !errors.Is(err, context.Canceled) {
					h.err = err
				}
				s <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
				// ignore unsupported control requests
			}
		case err := <-errCh:
			h.err = err
			s <- svc.Status{State: svc.StopPending}
			s <- svc.Status{State: svc.Stopped}
			if err != nil && !errors.Is(err, context.Canceled) {
				return false, 1
			}
			return false, 0
		}
	}
}

// RunWindowsService runs runFn inside the Windows Service Control Manager dispatcher.
func RunWindowsService(serviceName string, runFn func(ctx context.Context) error) error {
	handler := &winServiceHandler{runFn: runFn}
	if err := svc.Run(serviceName, handler); err != nil {
		return fmt.Errorf("running windows service %q: %w", serviceName, err)
	}
	if handler.err != nil && !errors.Is(handler.err, context.Canceled) {
		return handler.err
	}
	return nil
}

// InstallWindowsService installs the exporter as a background Windows Service under SCM.
func InstallWindowsService(w io.Writer, cfg ServiceConfig) error {
	cfg.setWindowsDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	execPath := resolveWindowsExecPath(cfg.ExecPath)
	args := windowsServiceArgs(cfg)

	logf("Binary path -> %s", execPath)
	logf("Arguments -> %v", args)
	if cfg.ConfigPath != "" {
		logf("ConfigPath -> %s", cfg.ConfigPath)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to service control manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(cfg.ServiceName)
	if err == nil {
		_ = s.Close()
		return fmt.Errorf("service %q already exists", cfg.ServiceName)
	}

	serviceConfig := mgr.Config{
		ServiceType: windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:   mgr.StartAutomatic,
		DisplayName: "GitLab Process History Exporter",
		Description: "GitLab Process History Exporter (Prometheus)",
	}
	if cfg.ServiceUser != "" && cfg.ServiceUser != "LocalSystem" {
		serviceConfig.ServiceStartName = cfg.ServiceUser
	}

	logf("creating service %s", cfg.ServiceName)
	s, err = m.CreateService(cfg.ServiceName, execPath, serviceConfig, args...)
	if err != nil {
		return fmt.Errorf("creating service %q: %w", cfg.ServiceName, err)
	}
	defer func() { _ = s.Close() }()

	recoveryActions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 20 * time.Second},
	}
	if err := s.SetRecoveryActions(recoveryActions, 86400); err != nil {
		logf("warning: configuring restart recovery actions: %v", err)
	}

	fmt.Fprintf(w, "\nDone. The %s service has been installed.\n", cfg.ServiceName)
	fmt.Fprintf(w, "  sc.exe query %s\n", cfg.ServiceName)
	return nil
}

// UninstallWindowsService removes the exporter service from Windows SCM.
func UninstallWindowsService(w io.Writer, cfg ServiceConfig) error {
	cfg.setWindowsDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to service control manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(cfg.ServiceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			logf("service %s is not installed; nothing to remove", cfg.ServiceName)
			return nil
		}
		return fmt.Errorf("opening service %q: %w", cfg.ServiceName, err)
	}
	defer func() { _ = s.Close() }()

	status, err := s.Query()
	if err == nil && status.State != svc.Stopped {
		logf("stopping service %s", cfg.ServiceName)
		if _, err := s.Control(svc.Stop); err != nil {
			logf("stop request reported: %v (continuing)", err)
		} else {
			timeout := time.Now().Add(10 * time.Second)
			for status.State != svc.Stopped && time.Now().Before(timeout) {
				time.Sleep(300 * time.Millisecond)
				status, err = s.Query()
				if err != nil {
					break
				}
			}
		}
	}

	logf("deleting service %s", cfg.ServiceName)
	if err := s.Delete(); err != nil {
		return fmt.Errorf("deleting service %q: %w", cfg.ServiceName, err)
	}

	fmt.Fprintf(w, "\nDone. The %s service has been removed.\n", cfg.ServiceName)
	return nil
}
