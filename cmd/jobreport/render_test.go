package main

import "testing"

func TestRenderTableGolden(t *testing.T) {
	got := renderTable([][]string{
		{"#", "proc", "x"},
		{"1", "java", "5"},
	}, 40)
	want := "" +
		"┌───┬──────┬───┐\n" +
		"│ # │ proc │ x │\n" +
		"├───┼──────┼───┤\n" +
		"│ 1 │ java │ 5 │\n" +
		"└───┴──────┴───┘\n"
	if got != want {
		t.Errorf("table mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderTruncate(t *testing.T) {
	got := renderTable([][]string{
		{"h"},
		{"abcdefghij"},
	}, 6)
	// cell truncated to 6 runes: "abc..." ; col width 6
	want := "" +
		"┌────────┐\n" +
		"│   h    │\n" +
		"├────────┤\n" +
		"│ abc... │\n" +
		"└────────┘\n"
	if got != want {
		t.Errorf("truncate mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestNum(t *testing.T) {
	cases := []struct {
		v    float64
		dec  int
		want string
	}{
		{5.9170, 2, "5.92"},
		{4.833, 2, "4.83"},
		{0.95, 1, "1"},
		{1.168, 1, "1.2"},
		{789.21, 1, "789.2"},
		{20444.0, 0, "20444"},
	}
	for _, c := range cases {
		if got := num(c.v, c.dec); got != c.want {
			t.Errorf("num(%v,%d) = %q, want %q", c.v, c.dec, got, c.want)
		}
	}
}
