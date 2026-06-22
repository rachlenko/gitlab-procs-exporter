package exporter

import (
	"strings"
	"testing"
	"time"

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
	// Process without pod UID or job name must not emit anything.
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
}
