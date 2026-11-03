//go:build !windows

package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestWindowsServiceStubsRejectNonWindows(t *testing.T) {
	if IsWindowsService() {
		t.Errorf("IsWindowsService on non-Windows returned true, want false")
	}

	err := RunWindowsService("test", func(ctx context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "only supported on Windows") {
		t.Errorf("RunWindowsService on non-Windows unexpected error: %v", err)
	}

	var buf bytes.Buffer
	err = InstallWindowsService(&buf, ServiceConfig{})
	if err == nil || !strings.Contains(err.Error(), "only supported on Windows") {
		t.Errorf("InstallWindowsService on non-Windows unexpected error: %v", err)
	}

	buf.Reset()
	err = UninstallWindowsService(&buf, ServiceConfig{})
	if err == nil || !strings.Contains(err.Error(), "only supported on Windows") {
		t.Errorf("UninstallWindowsService on non-Windows unexpected error: %v", err)
	}
}
