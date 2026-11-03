//go:build windows

package exporter

import (
	"testing"
)

func TestWindowsSelfIOReturnsZero(t *testing.T) {
	st, err := SelfIO("", 1234)
	if err != nil {
		t.Fatalf("SelfIO returned error on Windows: %v", err)
	}
	want := IOStats{}
	if st != want {
		t.Errorf("SelfIO = %+v, want %+v", st, want)
	}
}

func TestIOStatsAdd(t *testing.T) {
	var a IOStats
	b := IOStats{
		ReadBytes:     10,
		WriteBytes:    20,
		ReadSyscalls:  30,
		WriteSyscalls: 40,
	}
	a.add(b)
	if a != b {
		t.Errorf("IOStats.add = %+v, want %+v", a, b)
	}
}
