package deploy

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestServiceConfigDefaults(t *testing.T) {
	var c ServiceConfig
	c.setDefaults()
	if c.Module != Module {
		t.Errorf("Module = %q, want %q", c.Module, Module)
	}
	if c.Version != "latest" {
		t.Errorf("Version = %q, want latest", c.Version)
	}
	if c.InstallDir != "/usr/local/bin" {
		t.Errorf("InstallDir = %q", c.InstallDir)
	}
	if c.ServiceUser != "root" {
		t.Errorf("ServiceUser = %q", c.ServiceUser)
	}
	if c.Port != 8000 {
		t.Errorf("Port = %d, want 8000", c.Port)
	}
	if c.Interval != time.Minute {
		t.Errorf("Interval = %v, want 1m", c.Interval)
	}
	if got := c.binaryPath(); got != "/usr/local/bin/gitlab-procs-exporter" {
		t.Errorf("binaryPath = %q", got)
	}
}

func TestUnitPathUsesSystemdDir(t *testing.T) {
	orig := systemdDir
	t.Cleanup(func() { systemdDir = orig })
	systemdDir = "/etc/systemd/system"

	c := ServiceConfig{ServiceName: "myexp"}
	if got := c.unitPath(); got != "/etc/systemd/system/myexp.service" {
		t.Errorf("unitPath = %q", got)
	}
}

func TestRenderUnitFile(t *testing.T) {
	c := ServiceConfig{
		InstallDir:  "/opt/bin",
		Port:        9100,
		Interval:    90 * time.Second,
		ServiceUser: "root",
	}
	out, err := renderUnitFile(c)
	if err != nil {
		t.Fatalf("renderUnitFile: %v", err)
	}
	wants := []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"Type=simple",
		"ExecStart=/opt/bin/gitlab-procs-exporter --port=9100 --interval=1m30s",
		"User=root",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
		"Documentation=https://" + Module,
		"NoNewPrivileges=true",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("unit file missing %q\n---\n%s", w, out)
		}
	}
}

func TestInstallServiceRejectsNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this guard only triggers off Linux")
	}
	var buf bytes.Buffer
	err := InstallService(&buf, ServiceConfig{})
	if err == nil || !strings.Contains(err.Error(), "only supported on Linux") {
		t.Errorf("expected non-Linux rejection, got: %v", err)
	}
}

func TestUninstallServiceRejectsNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this guard only triggers off Linux")
	}
	var buf bytes.Buffer
	err := UninstallService(&buf, ServiceConfig{})
	if err == nil || !strings.Contains(err.Error(), "only supported on Linux") {
		t.Errorf("expected non-Linux rejection, got: %v", err)
	}
}

func TestRemoveIfPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := removeIfPresent(&buf, path); err != nil {
		t.Fatalf("removeIfPresent: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file should be gone, stat err = %v", err)
	}
	if !strings.Contains(buf.String(), "removed:") {
		t.Errorf("expected 'removed:' message, got %q", buf.String())
	}

	// Idempotent: a second call on a missing path is a no-op success.
	buf.Reset()
	if err := removeIfPresent(&buf, path); err != nil {
		t.Errorf("second call should be a no-op, got: %v", err)
	}
	if !strings.Contains(buf.String(), "not present") {
		t.Errorf("expected 'not present' message, got %q", buf.String())
	}
}
