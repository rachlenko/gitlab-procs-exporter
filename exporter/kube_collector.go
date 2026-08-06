package exporter

import "github.com/prometheus/client_golang/prometheus"

// KubeCollector emits per-GitLab-CI-job pod resource requests. It joins active
// processes (which carry the pod UID and CI_JOB_NAME) with the KubeStore.
type KubeCollector struct {
	store *HistoryStore
	kube  *KubeStore

	cpuDesc *prometheus.Desc
	memDesc *prometheus.Desc
}

// NewKubeCollector creates a KubeCollector.
func NewKubeCollector(store *HistoryStore, kube *KubeStore) *KubeCollector {
	labels := []string{"job_name"}
	return &KubeCollector{
		store: store,
		kube:  kube,
		cpuDesc: prometheus.NewDesc(
			"kuber_cpu_request",
			"CPU request of the GitLab CI job pod, in cores.",
			labels, nil,
		),
		memDesc: prometheus.NewDesc(
			"kuber_memory_request",
			"Memory request of the GitLab CI job pod, in bytes.",
			labels, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (kc *KubeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- kc.cpuDesc
	ch <- kc.memDesc
}

// Collect implements prometheus.Collector.
func (kc *KubeCollector) Collect(ch chan<- prometheus.Metric) {
	// Dedup by job_name: identical label sets would make Prometheus panic.
	byJob := make(map[string]KubePodInfo)
	for _, p := range kc.store.GetActiveProcesses() {
		if p.PodUID == "" {
			continue
		}
		// Same /proc-sourced value as the ci_job_name label, so it gets the same
		// treatment and the same limit. sanitizeLabelValue is not optional:
		// MustNewConstMetric panics on invalid UTF-8 and this collector shares
		// the registry (and therefore the gather goroutine) with
		// ProcessCollector, so a bad byte here takes the whole exporter down.
		// Bounding happens BEFORE the dedup key so two over-long names that
		// would collapse to one label set can't produce duplicate series.
		jobName := boundLabelWith(MaxLabelBytes, "ci_job_name",
			sanitizeLabelValue(p.Environ["CI_JOB_NAME"]), nil)
		if jobName == "" {
			continue
		}
		info, ok := kc.kube.Get(p.PodUID)
		if !ok {
			continue
		}
		byJob[jobName] = info // last-wins on collision
	}

	for jobName, info := range byJob {
		ch <- prometheus.MustNewConstMetric(kc.cpuDesc, prometheus.GaugeValue, info.CPURequest, jobName)
		ch <- prometheus.MustNewConstMetric(kc.memDesc, prometheus.GaugeValue, info.MemRequest, jobName)
	}
}
