//go:build windows

package deploy

import (
	"reflect"
	"testing"
	"time"
)

func TestWindowsServiceConfigDefaults(t *testing.T) {
	var c ServiceConfig
	c.setWindowsDefaults()
	if c.ServiceName != binaryName {
		t.Errorf("ServiceName = %q, want %q", c.ServiceName, binaryName)
	}
	if c.ServiceUser != "LocalSystem" {
		t.Errorf("ServiceUser = %q, want LocalSystem", c.ServiceUser)
	}
	if c.Port != 8000 {
		t.Errorf("Port = %d, want 8000", c.Port)
	}
	if c.Interval != time.Minute {
		t.Errorf("Interval = %v, want 1m", c.Interval)
	}
}

func TestWindowsServiceConfigCustomValuesPreserved(t *testing.T) {
	c := ServiceConfig{
		ServiceName: "custom-exporter",
		ServiceUser: "NT AUTHORITY\\LocalService",
		Port:        9090,
		Interval:    30 * time.Second,
		ConfigPath:  "C:\\config\\rules.yml",
		ExecPath:    "C:\\tools\\gitlab-procs-exporter.exe",
	}
	c.setWindowsDefaults()
	if c.ServiceName != "custom-exporter" {
		t.Errorf("ServiceName = %q, want custom-exporter", c.ServiceName)
	}
	if c.ServiceUser != "NT AUTHORITY\\LocalService" {
		t.Errorf("ServiceUser = %q, want NT AUTHORITY\\LocalService", c.ServiceUser)
	}
	if c.Port != 9090 {
		t.Errorf("Port = %d, want 9090", c.Port)
	}
	if c.Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", c.Interval)
	}
	if c.ConfigPath != "C:\\config\\rules.yml" {
		t.Errorf("ConfigPath = %q, want C:\\config\\rules.yml", c.ConfigPath)
	}
	if c.ExecPath != "C:\\tools\\gitlab-procs-exporter.exe" {
		t.Errorf("ExecPath = %q, want C:\\tools\\gitlab-procs-exporter.exe", c.ExecPath)
	}
}

func TestWindowsServiceArgs(t *testing.T) {
	c := ServiceConfig{
		Port:     9100,
		Interval: 90 * time.Second,
	}
	got := windowsServiceArgs(c)
	want := []string{"--port=9100", "--interval=1m30s"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("windowsServiceArgs = %v, want %v", got, want)
	}
}

func TestWindowsServiceArgsWithConfigPath(t *testing.T) {
	c := ServiceConfig{
		Port:       8000,
		Interval:   time.Minute,
		ConfigPath: "C:\\Program Files\\GitLab\\config.yml",
	}
	got := windowsServiceArgs(c)
	want := []string{"--port=8000", "--interval=1m0s", "--config=C:\\Program Files\\GitLab\\config.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("windowsServiceArgs = %v, want %v", got, want)
	}
}

func TestResolveWindowsExecPath(t *testing.T) {
	configured := "C:\\custom\\path\\bin.exe"
	if got := resolveWindowsExecPath(configured); got != configured {
		t.Errorf("resolveWindowsExecPath(%q) = %q, want %q", configured, got, configured)
	}

	auto := resolveWindowsExecPath("")
	if auto == "" {
		t.Errorf("resolveWindowsExecPath(\"\") returned empty string")
	}
}
