package exporter

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestKubeCollector(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp: time.Now(),
		PID:       100,
		Name:      "ruby",
		PodUID:    "pod-aaa",
		Environ:   map[string]string{"CI_JOB_NAME": "build"},
		IsActive:  true,
	})
	// Process with neither PodUID nor CI_JOB_NAME must not emit anything.
	store.AddSample(ProcessSample{
		Timestamp: time.Now(),
		PID:       101,
		Name:      "bash",
		Environ:   map[string]string{},
		IsActive:  true,
	})

	ks := NewKubeStore()
	ks.Replace([]KubePodInfo{{UID: "pod-aaa", CPURequest: 0.5, MemRequest: 1048576}})

	c := NewKubeCollector(store, ks)

	expected := `
# HELP kuber_cpu_request CPU request of the GitLab CI job pod, in cores.
# TYPE kuber_cpu_request gauge
kuber_cpu_request{job_name="build"} 0.5
# HELP kuber_memory_request Memory request of the GitLab CI job pod, in bytes.
# TYPE kuber_memory_request gauge
kuber_memory_request{job_name="build"} 1.048576e+06
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected)); err != nil {
		t.Error(err)
	}
}

func TestKubeCollectorDedupJobName(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp: time.Now(), PID: 1, Name: "a", PodUID: "pod-aaa",
		Environ: map[string]string{"CI_JOB_NAME": "test"}, IsActive: true,
	})
	store.AddSample(ProcessSample{
		Timestamp: time.Now(), PID: 2, Name: "b", PodUID: "pod-bbb",
		Environ: map[string]string{"CI_JOB_NAME": "test"}, IsActive: true,
	})
	ks := NewKubeStore()
	ks.Replace([]KubePodInfo{
		{UID: "pod-aaa", CPURequest: 0.5, MemRequest: 1},
		{UID: "pod-bbb", CPURequest: 0.9, MemRequest: 2},
	})
	c := NewKubeCollector(store, ks)
	// Same job_name on two pods must collapse to exactly one series per metric.
	if n := testutil.CollectAndCount(c, "kuber_cpu_request"); n != 1 {
		t.Errorf("expected 1 kuber_cpu_request series, got %d", n)
	}
	if n := testutil.CollectAndCount(c, "kuber_memory_request"); n != 1 {
		t.Errorf("expected 1 kuber_memory_request series, got %d", n)
	}
}

// job_name comes from /proc/<pid>/environ, so it carries exactly the hazards
// every other /proc-sourced label does — and this collector shares a registry,
// and therefore a gather goroutine, with ProcessCollector. Invalid UTF-8 here
// panics MustNewConstMetric and takes the whole exporter down; an unbounded
// value costs index memory on two more metrics. Neither is visible in the
// happy-path test above because it uses a short, clean job name.
func TestKubeCollectorSanitizesAndBoundsJobName(t *testing.T) {
	limit := MaxLabelBytes["ci_job_name"]

	tests := []struct {
		name    string
		jobName string
		check   func(t *testing.T, got string)
	}{
		{
			name:    "invalid UTF-8 is sanitized",
			jobName: "build\xffstep",
			check: func(t *testing.T, got string) {
				if !utf8.ValidString(got) {
					t.Errorf("job_name is not valid UTF-8: %q — MustNewConstMetric panics on this", got)
				}
			},
		},
		{
			name:    "over-long value is bounded",
			jobName: strings.Repeat("j", 4096),
			check: func(t *testing.T, got string) {
				if ceiling := limit + maxMarkerLen; len(got) > ceiling {
					t.Errorf("job_name is %d bytes, ceiling is %d", len(got), ceiling)
				}
				if !strings.Contains(got, ";sha256=") {
					t.Errorf("a 4KB job name must be truncated, got %q", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewHistoryStore()
			store.AddSample(ProcessSample{
				Timestamp: time.Now(),
				PID:       200,
				Name:      "ruby",
				PodUID:    "pod-bbb",
				Environ:   map[string]string{"CI_JOB_NAME": tt.jobName},
				IsActive:  true,
			})
			ks := NewKubeStore()
			ks.Replace([]KubePodInfo{{UID: "pod-bbb", CPURequest: 0.5, MemRequest: 1048576}})

			// Gather through a pedantic registry rather than calling Collect
			// directly: that is what turns a bad label value into the panic this
			// test exists to prevent.
			reg := prometheus.NewPedanticRegistry()
			reg.MustRegister(NewKubeCollector(store, ks))
			mfs, err := reg.Gather()
			if err != nil {
				t.Fatalf("Gather: %v", err)
			}

			var got string
			var found bool
			for _, mf := range mfs {
				if mf.GetName() != "kuber_cpu_request" {
					continue
				}
				for _, m := range mf.GetMetric() {
					for _, lp := range m.GetLabel() {
						if lp.GetName() == "job_name" {
							got, found = lp.GetValue(), true
						}
					}
				}
			}
			if !found {
				t.Fatal("no kuber_cpu_request series carried a job_name label")
			}
			tt.check(t, got)
		})
	}
}
