package deploy

import (
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
	Module      string        // module path passed to `go install`
	Version     string        // module version, e.g. "latest" or "v0.0.2"
	BinName     string        // installed binary name
	InstallDir  string        // directory the binary is installed into
	ServiceName string        // systemd unit name (without ".service")
	ServiceUser string        // User= the service runs as
	Port        int           // --port passed to the exporter
	Interval    time.Duration // --interval passed to the exporter
}

func (c *ServiceConfig) setDefaults() {
	if c.Module == "" {
		c.Module = Module
	}
	if c.Version == "" {
		c.Version = "latest"
	}
	if c.BinName == "" {
		c.BinName = "gitlab-procs-exporter"
	}
	if c.InstallDir == "" {
		c.InstallDir = "/usr/local/bin"
	}
	if c.ServiceName == "" {
		c.ServiceName = "gitlab-procs-exporter"
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

func (c ServiceConfig) binaryPath() string {
	return filepath.Join(c.InstallDir, c.BinName)
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
ExecStart={{.BinaryPath}} --port={{.Port}} --interval={{.Interval}}
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
		BinaryPath  string
		Port        int
		Interval    string
		ServiceUser string
	}{
		Module:      cfg.Module,
		BinaryPath:  cfg.binaryPath(),
		Port:        cfg.Port,
		Interval:    cfg.Interval.String(),
		ServiceUser: cfg.ServiceUser,
	}
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// InstallService builds the exporter with `go install` and installs it as a
// systemd service: writes the unit file, then daemon-reload / enable --now.
// Progress is written to w. It requires Linux and root privileges.
func InstallService(w io.Writer, cfg ServiceConfig) error {
	cfg.setDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd deployment is only supported on Linux (this host is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (try: sudo %s --deploy-as-systemd-service)", cfg.BinName)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found; this installer targets systemd-based systems: %w", err)
	}
	goBin, err := findGo()
	if err != nil {
		return err
	}
	logf("using go: %s", goBin)

	if cfg.ServiceUser != "root" {
		if _, err := exec.Command("id", "--", cfg.ServiceUser).CombinedOutput(); err != nil {
			return fmt.Errorf("service user %q does not exist (create it or use ServiceUser=root)", cfg.ServiceUser)
		}
	}

	logf("installing %s@%s -> %s", cfg.Module, cfg.Version, cfg.binaryPath())
	if err := goInstall(w, goBin, cfg); err != nil {
		return err
	}
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

// goInstall runs `go install <module>@<version>` into cfg.InstallDir and
// confirms the binary landed. GOBIN pins the output location; GOFLAGS is
// cleared so caller flags do not leak in; GOTOOLCHAIN=auto lets go fetch a
// newer toolchain when the module requires one.
func goInstall(w io.Writer, goBin string, cfg ServiceConfig) error {
	if err := os.MkdirAll(cfg.InstallDir, 0o755); err != nil {
		return fmt.Errorf("creating install dir %s: %w", cfg.InstallDir, err)
	}
	cmd := exec.Command(goBin, "install", cfg.Module+"@"+cfg.Version)
	cmd.Env = append(os.Environ(), "GOBIN="+cfg.InstallDir, "GOFLAGS=", "GOTOOLCHAIN=auto")
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go install failed: %w", err)
	}
	if _, err := os.Stat(cfg.binaryPath()); err != nil {
		return fmt.Errorf("binary not found after install at %s: %w", cfg.binaryPath(), err)
	}
	return nil
}

// writeUnit renders and writes the systemd unit file for cfg.
func writeUnit(w io.Writer, cfg ServiceConfig) error {
	unit, err := renderUnitFile(cfg)
	if err != nil {
		return fmt.Errorf("rendering unit file: %w", err)
	}
	fmt.Fprintf(w, "==> writing unit file: %s\n", cfg.unitPath())
	if err := os.WriteFile(cfg.unitPath(), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing unit file: %w", err)
	}
	return nil
}

// UninstallService reverses InstallService: it stops and disables the systemd
// unit, removes the unit file, reloads systemd, and deletes the installed
// binary. It is idempotent — artifacts that are already gone are reported and
// skipped rather than treated as errors. Requires Linux and root.
//
// Only the unit file and the binary this tool installs are touched; the
// exporter is flag-configured and has no other on-disk config to remove.
func UninstallService(w io.Writer, cfg ServiceConfig) error {
	cfg.setDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd uninstall is only supported on Linux (this host is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (try: sudo %s --uninstall)", cfg.BinName)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found; this targets systemd-based systems: %w", err)
	}

	unit := cfg.ServiceName + ".service"

	// Stop + disable. Best-effort: the unit may already be absent.
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

	if err := removeIfPresent(w, cfg.binaryPath()); err != nil {
		return err
	}

	fmt.Fprintf(w, "\nDone. %s has been removed.\n", cfg.ServiceName)
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
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// findGo locates the go toolchain, probing common install locations because
// root's PATH under sudo often omits where Go was installed.
func findGo() (string, error) {
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	cands := []string{"/usr/local/go/bin/go", "/usr/lib/go/bin/go", "/snap/bin/go"}
	if su := os.Getenv("SUDO_USER"); su != "" {
		cands = append(cands, filepath.Join("/home", su, "go", "bin", "go"))
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("go toolchain not found; run --check-dependencies first")
}
