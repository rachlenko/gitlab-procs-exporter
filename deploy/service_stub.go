//go:build !windows

package deploy

import (
	"context"
	"fmt"
	"io"
)

// IsWindowsService reports whether the current process is running as a Windows Service under SCM.
// On non-Windows platforms, this always returns false.
func IsWindowsService() bool {
	return false
}

// RunWindowsService runs the given function as a Windows service.
// On non-Windows platforms, it returns an error indicating that Windows is required.
func RunWindowsService(serviceName string, runFn func(ctx context.Context) error) error {
	return fmt.Errorf("windows services are only supported on Windows")
}

// InstallWindowsService installs the exporter as a Windows service under SCM.
// On non-Windows platforms, it returns an error indicating that Windows is required.
func InstallWindowsService(w io.Writer, cfg ServiceConfig) error {
	return fmt.Errorf("windows service installation is only supported on Windows")
}

// UninstallWindowsService removes the exporter from Windows SCM.
// On non-Windows platforms, it returns an error indicating that Windows is required.
func UninstallWindowsService(w io.Writer, cfg ServiceConfig) error {
	return fmt.Errorf("windows service uninstallation is only supported on Windows")
}
