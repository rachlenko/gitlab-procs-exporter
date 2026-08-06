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

// job_name and ci_job_name are the SAME CI_JOB_NAME value emitted on one
// registry, so an operator override of ci_job_name has to move both. If it
// moves only ci_job_name, the two labels carry different renderings of one
// value and any PromQL joining kuber_* to gitlab_process_* on job name
// silently matches nothing — a query that worked before the override stops
// returning rows, with no error anywhere to explain it.
//
// The test above cannot catch this: it reads MaxLabelBytes["ci_job_name"]
// directly, so it passes whether the collector honours the override or ignores
// it. This one asserts the override is actually applied, and that the cut is
// counted rather than dropped on the floor.
func TestKubeCollectorHonoursJobNameOverride(t *testing.T) {
	const override = 512
	jobName := strings.Repeat("j", 400) // over the 256 default, under the override

	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp: time.Now(), PID: 300, Name: "ruby", PodUID: "pod-ccc",
		Environ: map[string]string{"CI_JOB_NAME": jobName}, IsActive: true,
	})
	ks := NewKubeStore()
	ks.Replace([]KubePodInfo{{UID: "pod-ccc", CPURequest: 0.5, MemRequest: 1048576}})

	cfg := &Config{MaxLabelBytes: map[string]int{"ci_job_name": override}}
	pc := NewProcessCollectorWithConfig(store, cfg)
	kc := NewKubeCollectorWithConfig(store, ks, cfg, pc)

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(kc)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var got string
	for _, mf := range mfs {
		if mf.GetName() != "kuber_cpu_request" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "job_name" {
					got = lp.GetValue()
				}
			}
		}
	}
	if got != jobName {
		t.Errorf("job_name must follow the ci_job_name override to %d and pass a %d-byte "+
			"value through untruncated; got %d bytes (%q)", override, len(jobName), len(got), got)
	}

	// Same value on the process side, so the two must agree exactly — that
	// agreement is the whole point of sharing the limit.
	if bounded := pc.boundLabel("ci_job_name", jobName); bounded != got {
		t.Errorf("job_name %q disagrees with ci_job_name %q for the same source value", got, bounded)
	}

	// A cut below the override must still be counted. Re-run past the override
	// so truncation actually fires.
	long := strings.Repeat("k", override+1)
	store2 := NewHistoryStore()
	store2.AddSample(ProcessSample{
		Timestamp: time.Now(), PID: 301, Name: "ruby", PodUID: "pod-ccc",
		Environ: map[string]string{"CI_JOB_NAME": long}, IsActive: true,
	})
	pc2 := NewProcessCollectorWithConfig(store2, cfg)
	kc2 := NewKubeCollectorWithConfig(store2, ks, cfg, pc2)
	if n := testutil.CollectAndCount(kc2, "kuber_cpu_request"); n != 1 {
		t.Fatalf("expected 1 series, got %d", n)
	}
	if n := testutil.ToFloat64(pc2.truncations.WithLabelValues("ci_job_name")); n == 0 {
		t.Error("kuber_* job_name truncation was not counted; the documented " +
			"'watch gitlab_exporter_label_truncations_total' workflow reports nothing for it")
	}
}

// A KubeCollector built without a ProcessCollector must not panic when it cuts
// a value. Passing a nil *ProcessCollector into an interface field yields a
// non-nil interface holding a nil pointer, which slips past the nil guard in
// boundLabelWith and panics on the gather goroutine — taking the exporter down.
func TestKubeCollectorNilObserverDoesNotPanic(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp: time.Now(), PID: 302, Name: "ruby", PodUID: "pod-ddd",
		Environ: map[string]string{"CI_JOB_NAME": strings.Repeat("j", 4096)}, IsActive: true,
	})
	ks := NewKubeStore()
	ks.Replace([]KubePodInfo{{UID: "pod-ddd", CPURequest: 0.5, MemRequest: 1}})

	kc := NewKubeCollectorWithConfig(store, ks, nil, nil)
	if n := testutil.CollectAndCount(kc, "kuber_cpu_request"); n != 1 {
		t.Errorf("expected 1 series, got %d", n)
	}
}
