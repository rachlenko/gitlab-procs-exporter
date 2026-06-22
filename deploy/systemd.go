package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"
)

// systemdDir is where unit files live, per
// https://wiki.debian.org/systemd/Services. Overridable in tests.
var systemdDir = "/etc/systemd/system"

// ServiceConfig describes the systemd service to install. Zero-value fields
// fall back to sensible defaults via setDefaults.
type ServiceConfig struct {
	ExecPath    string        // absolute path written to ExecStart (auto-resolved if empty)
	ServiceName string        // systemd unit name (without ".service")
	ServiceUser string        // User= the service runs as
	Port        int           // --port passed to the exporter
	Interval    time.Duration // --interval passed to the exporter
	ConfigPath  string        // --config passed to the exporter (omitted when empty)
}

func (c *ServiceConfig) setDefaults() {
	if c.ServiceName == "" {
		c.ServiceName = binaryName
	}
	if c.ServiceUser == "" {
		c.ServiceUser = "root"
	}
	if c.Port == 0 {
		c.Port = 8000
	}
	if c.Interval == 0 {
		c.Interval = time.Minute
	}
}

// resolveExecPath returns the path to write into ExecStart: the configured
// value, else the running executable (symlinks resolved), else the default
// /usr/bin/<binaryName> deb install location.
func resolveExecPath(configured string) string {
	if configured != "" {
		return configured
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
		return exe
	}
	return "/usr/bin/" + binaryName
}

func (c ServiceConfig) unitPath() string {
	return filepath.Join(systemdDir, c.ServiceName+".service")
}

// unitTemplate follows the [Unit]/[Service]/[Install] structure described in
// the Debian systemd/Services wiki. The hardening directives are chosen to
// stay compatible with reading /proc for every process (the exporter's job).
const unitTemplate = `[Unit]
Description=GitLab Process History Exporter (Prometheus)
Documentation=https://{{.Module}}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.ExecPath}} --port={{.Port}} --interval={{.Interval}}{{if .ConfigPath}} --config {{.ConfigPath}}{{end}}
User={{.ServiceUser}}
Restart=on-failure
RestartSec=5

# Hardening (kept compatible with reading /proc of all processes)
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectControlGroups=true
ProtectKernelModules=true
ProtectKernelTunables=true
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=true
LockPersonality=true

[Install]
WantedBy=multi-user.target
`

// renderUnitFile produces the unit file contents for cfg.
func renderUnitFile(cfg ServiceConfig) (string, error) {
	cfg.setDefaults()
	t, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	data := struct {
		Module      string
		ExecPath    string
		Port        int
		Interval    string
		ServiceUser string
		ConfigPath  string
	}{
		Module:      Module,
		ExecPath:    resolveExecPath(cfg.ExecPath),
		Port:        cfg.Port,
		Interval:    cfg.Interval.String(),
		ServiceUser: cfg.ServiceUser,
		ConfigPath:  cfg.ConfigPath,
	}
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// InstallService installs the exporter as a systemd service: it writes a unit
// file whose ExecStart points at the running binary, then daemon-reload /
// enable --now. The binary itself is expected to already be installed (e.g.
// via the .deb). Requires Linux and root privileges.
func InstallService(w io.Writer, cfg ServiceConfig) error {
	cfg.setDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd deployment is only supported on Linux (this host is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (try: sudo %s --deploy-as-systemd-service)", binaryName)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found; this targets systemd-based systems: %w", err)
	}
	if cfg.ServiceUser != "root" {
		if _, err := exec.CommandContext(context.Background(), "id", "--", cfg.ServiceUser).CombinedOutput(); err != nil {
			return fmt.Errorf("service user %q does not exist (create it or use ServiceUser=root)", cfg.ServiceUser)
		}
	}

	logf("ExecStart -> %s", resolveExecPath(cfg.ExecPath))
	if err := writeUnit(w, cfg); err != nil {
		return err
	}

	logf("reloading systemd and enabling the service")
	if err := runCmd(w, systemctl, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if err := runCmd(w, systemctl, "enable", "--now", cfg.ServiceName+".service"); err != nil {
		return fmt.Errorf("systemctl enable --now: %w", err)
	}
	_ = runCmd(w, systemctl, "--no-pager", "--full", "status", cfg.ServiceName+".service")

	fmt.Fprintf(w, "\nDone. The exporter is running on port %d.\n", cfg.Port)
	fmt.Fprintf(w, "  systemctl status %s\n", cfg.ServiceName)
	fmt.Fprintf(w, "  journalctl -u %s -f\n", cfg.ServiceName)
	return nil
}

// writeUnit renders and writes the systemd unit file for cfg.
func writeUnit(w io.Writer, cfg ServiceConfig) error {
	unit, err := renderUnitFile(cfg)
	if err != nil {
		return fmt.Errorf("rendering unit file: %w", err)
	}
	fmt.Fprintf(w, "==> writing unit file: %s\n", cfg.unitPath())
	// systemd unit files are world-readable by convention (0644); they hold no secrets.
	if err := os.WriteFile(cfg.unitPath(), []byte(unit), 0o644); err != nil { //nolint:gosec // G306: unit files are world-readable by convention
		return fmt.Errorf("writing unit file: %w", err)
	}
	return nil
}

// UninstallService removes the systemd service and the dpkg package: it stops
// and disables the unit, removes the unit file, reloads systemd, and then runs
// `dpkg -r` to remove the package that owns the binary. It is idempotent —
// missing artifacts and an already-removed package are skipped, not errors.
// Requires Linux and root.
func UninstallService(w io.Writer, cfg ServiceConfig) error {
	cfg.setDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd uninstall is only supported on Linux (this host is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (try: sudo %s --uninstall)", binaryName)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found; this targets systemd-based systems: %w", err)
	}

	unit := cfg.ServiceName + ".service"

	logf("stopping and disabling %s", unit)
	if err := runCmd(w, systemctl, "disable", "--now", unit); err != nil {
		logf("disable --now reported: %v (continuing)", err)
	}
	if err := removeIfPresent(w, cfg.unitPath()); err != nil {
		return err
	}
	logf("reloading systemd")
	_ = runCmd(w, systemctl, "daemon-reload")
	_ = runCmd(w, systemctl, "reset-failed", unit)

	// Remove the dpkg package that owns the binary. `dpkg -r` is the correct,
	// DB-safe way to delete a package-managed file (a manual rm would leave the
	// dpkg database inconsistent).
	if dpkg, err := exec.LookPath("dpkg"); err == nil {
		if dpkgInstalled(dpkg, binaryName) {
			logf("removing dpkg package %s", binaryName)
			if err := runCmd(w, dpkg, "-r", binaryName); err != nil {
				return fmt.Errorf("dpkg -r %s: %w", binaryName, err)
			}
		} else {
			logf("dpkg package %s is not installed; nothing to remove", binaryName)
		}
	} else {
		logf("dpkg not found; if the binary was installed outside dpkg, remove it manually")
	}

	fmt.Fprintf(w, "\nDone. The %s service and package have been removed.\n", cfg.ServiceName)
	return nil
}

// removeIfPresent deletes path, treating an already-missing path as success.
func removeIfPresent(w io.Writer, path string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(w, "==> not present (skipped): %s\n", path)
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	fmt.Fprintf(w, "==> removed: %s\n", path)
	return nil
}

func runCmd(w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}
