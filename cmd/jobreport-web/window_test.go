package main

import "testing"

func TestBuildWindow_AllEmpty(t *testing.T) {
	got, err := buildWindow("", "", "", "", "", "")
	if err != nil {
		t.Fatalf("all-empty: unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("all-empty: want %q, got %q", "", got)
	}
}

func TestBuildWindow_FullValid(t *testing.T) {
	// 2026-01-02 00:00:00 UTC .. 2026-01-02 01:30:00 UTC
	const wantStart = 1767312000
	const wantEnd = 1767317400
	want := "1767312000..1767317400"

	got, err := buildWindow("2026-01-02", "0", "0", "2026-01-02", "1", "30")
	if err != nil {
		t.Fatalf("full-valid: unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("full-valid: want %q (start=%d end=%d), got %q", want, wantStart, wantEnd, got)
	}
}

func TestBuildWindow_Errors(t *testing.T) {
	cases := []struct {
		name                   string
		sd, sh, sm, ed, eh, em string
	}{
		{"partial start", "2026-01-02", "0", "", "2026-01-02", "1", "30"},
		{"partial end", "2026-01-02", "0", "0", "2026-01-02", "1", ""},
		{"only one field", "", "", "", "2026-01-02", "", ""},
		{"end before start", "2026-01-02", "5", "0", "2026-01-02", "1", "0"},
		{"end equals start", "2026-01-02", "1", "0", "2026-01-02", "1", "0"},
		{"bad start date", "2026-13-40", "0", "0", "2026-01-02", "1", "30"},
		{"bad hour", "2026-01-02", "24", "0", "2026-01-02", "1", "30"},
		{"bad minute", "2026-01-02", "0", "99", "2026-01-02", "1", "30"},
		{"non-numeric hour", "2026-01-02", "ab", "0", "2026-01-02", "1", "30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildWindow(tc.sd, tc.sh, tc.sm, tc.ed, tc.eh, tc.em)
			if err == nil {
				t.Fatalf("%s: want error, got nil (result %q)", tc.name, got)
			}
		})
	}
}
