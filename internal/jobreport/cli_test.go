package jobreport

import "testing"

func TestParseFlagsPositionalAndFlagOrder(t *testing.T) {
	// Flags must apply whether they come before or after the positional job-log
	// source — Go's flag package alone stops at the first positional.
	cases := []struct {
		name string
		argv []string
	}{
		{"flags before url", []string{"-prom", "https://p.example/", "-top", "9", "https://h/job.log"}},
		{"flags after url", []string{"https://h/job.log", "-prom", "https://p.example/", "-top", "9"}},
		{"flags around url", []string{"-prom", "https://p.example/", "https://h/job.log", "-top", "9"}},
		{"equals form", []string{"https://h/job.log", "-prom=https://p.example/", "-top=9"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := parseFlags(c.argv)
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			if cfg.jobURL != "https://h/job.log" {
				t.Errorf("jobURL = %q, want https://h/job.log", cfg.jobURL)
			}
			if cfg.promURL != "https://p.example/" {
				t.Errorf("promURL = %q, want https://p.example/", cfg.promURL)
			}
			if cfg.topN != 9 {
				t.Errorf("topN = %d, want 9", cfg.topN)
			}
		})
	}
}

func TestParseWindowArg(t *testing.T) {
	t.Run("relative", func(t *testing.T) {
		s, e, err := parseWindow("10m")
		if err != nil {
			t.Fatalf("parseWindow: %v", err)
		}
		if e-s != 600 {
			t.Errorf("span = %ds, want 600", e-s)
		}
	})
	t.Run("absolute ok", func(t *testing.T) {
		s, e, err := parseWindow("100..200")
		if err != nil {
			t.Fatalf("parseWindow: %v", err)
		}
		if s != 100 || e != 200 {
			t.Errorf("got %d..%d, want 100..200", s, e)
		}
	})
	t.Run("absolute reversed rejected", func(t *testing.T) {
		if _, _, err := parseWindow("200..100"); err == nil {
			t.Error("parseWindow(200..100): want error, got nil")
		}
	})
	t.Run("absolute equal rejected", func(t *testing.T) {
		if _, _, err := parseWindow("100..100"); err == nil {
			t.Error("parseWindow(100..100): want error, got nil")
		}
	})
}

func TestParseFlagsLocalFilePositional(t *testing.T) {
	cfg, err := parseFlags([]string{"testdata/job.log", "-top", "3"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.jobURL != "testdata/job.log" {
		t.Errorf("jobURL = %q, want testdata/job.log", cfg.jobURL)
	}
	if cfg.topN != 3 {
		t.Errorf("topN = %d, want 3", cfg.topN)
	}
}
