package exporter

import "github.com/prometheus/client_golang/prometheus"

// KubeCollector emits per-GitLab-CI-job pod resource requests. It joins active
// processes (which carry the pod UID and CI_JOB_NAME) with the KubeStore.
type KubeCollector struct {
	store *HistoryStore
	kube  *KubeStore

	// maxLabelBytes is this collector's own copy of the limit table, for the
	// same reason ProcessCollector keeps one: reading the package-level
	// MaxLabelBytes live on the gather goroutine races with any writer to it,
	// and a concurrent map read/write is an unrecoverable fatal error that takes
	// the exporter down.
	maxLabelBytes map[string]int
	// obs counts the cuts this collector makes. nil is supported (tests without
	// a registry); production wires the ProcessCollector so job_name truncation
	// lands in gitlab_exporter_label_truncations_total instead of vanishing.
	obs truncationObserver

	cpuDesc *prometheus.Desc
	memDesc *prometheus.Desc
}

// NewKubeCollector creates a KubeCollector with the DEFAULT label-size
// contract and no truncation counting. See NewKubeCollectorWithConfig, which is
// what production uses — job_name shares CI_JOB_NAME with ci_job_name, so it
// must share that label's operator override too, or the same source value gets
// emitted at two different limits on the same registry.
func NewKubeCollector(store *HistoryStore, kube *KubeStore) *KubeCollector {
	labels := kubeLabelNames()
	return &KubeCollector{
		store:         store,
		kube:          kube,
		maxLabelBytes: mergedMaxLabelBytes(nil),
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

// NewKubeCollectorWithConfig creates a KubeCollector that honours the
// operator's max_label_bytes overrides and counts its truncations.
//
// pc is the ProcessCollector this collector shares a registry with; it owns the
// truncation counter. Cuts here are counted under "ci_job_name", not
// "job_name", because that is the limit being applied — a separate series would
// suggest a separate limit an operator could tune, and there isn't one.
//
// Both arguments are optional: a nil cfg means "defaults", and a nil pc means
// "don't count". The nil check on pc is load-bearing — assigning a nil
// *ProcessCollector straight into the interface field would yield a non-nil
// interface holding a nil pointer, so the nil guard in boundLabelWith would not
// fire and the counter increment would panic on the gather goroutine.
func NewKubeCollectorWithConfig(store *HistoryStore, kube *KubeStore, cfg *Config, pc *ProcessCollector) *KubeCollector {
	kc := NewKubeCollector(store, kube)
	if cfg != nil {
		kc.maxLabelBytes = mergedMaxLabelBytes(cfg.MaxLabelBytes)
	}
	if pc != nil {
		kc.obs = pc
	}
	return kc
}

// kubeLabelNames is this collector's full label set. It is a function rather
// than an inline literal for the same reason infoLabelNames is: the contract
// guard test asserts that every label this exporter emits is either bounded or
// explicitly exempted, and it can only do that over an enumerable set. This
// collector is precisely where that failure already happened once — job_name
// shipped raw, unsanitized and unbounded — so a second kuber_* label must not
// be able to pass through unnoticed.
func kubeLabelNames() []string {
	return []string{"job_name"}
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
		jobName := boundLabelWith(kc.maxLabelBytes, "ci_job_name",
			sanitizeLabelValue(p.Environ["CI_JOB_NAME"]), kc.obs)
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
