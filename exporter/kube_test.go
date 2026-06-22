package exporter

import "testing"

func TestParseCPUQuantity(t *testing.T) {
	cases := map[string]float64{
		"500m": 0.5, "250m": 0.25, "1": 1, "2": 2, "1500m": 1.5, "": 0,
	}
	for in, want := range cases {
		got, err := ParseCPUQuantity(in)
		if err != nil {
			t.Errorf("ParseCPUQuantity(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseCPUQuantity(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseMemoryQuantity(t *testing.T) {
	cases := map[string]float64{
		"512Mi": 536870912, "1Gi": 1073741824, "128M": 128000000,
		"1000": 1000, "2Ki": 2048, "": 0,
	}
	for in, want := range cases {
		got, err := ParseMemoryQuantity(in)
		if err != nil {
			t.Errorf("ParseMemoryQuantity(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMemoryQuantity(%q) = %v, want %v", in, got, want)
		}
	}
}
