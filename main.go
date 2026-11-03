package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/rachlenko/gitlab-procs-exporter/deploy"
	"github.com/rachlenko/gitlab-procs-exporter/exporter"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shirou/gopsutil/v3/process"
)

//go:embed dashboard/index.html
var dashboardHTML embed.FS

// revision is injected at build time via -ldflags "-X main.revision=...".
// When the binary is produced by `go install module@version`, it stays "dev"
// here and the real version is recovered from the embedded build info instead.
var revision = "dev"

type cliOptions struct {
	port            int
	scrapeInterval  time.Duration
	showVersion     bool
	checkDeps       bool
	deploySystemd   bool
	deployWindows   bool
	deployService   bool
	uninstall       bool
	update          bool
	serviceName     string
	serviceUser     string
	kubeletInsecure bool
	configPath      string
}

type actionType int

const (
	actionRun actionType = iota
	actionVersion
	actionCheckDeps
	actionDeploySystemd
	actionDeployWindows
	actionUninstallSystemd
	actionUninstallWindows
	actionUpdate
)

func defaultServiceUser(goos string) string {
	if goos == "windows" {
		return "LocalSystem"
	}
	return "root"
}

func parseFlags(fs *flag.FlagSet, args []string, goos string) (*cliOptions, error) {
	opts := &cliOptions{}
	fs.IntVar(&opts.port, "port", 8000, "Port to run the exporter on")
	fs.DurationVar(&opts.scrapeInterval, "interval", 10*time.Second, "Scrape interval")
	fs.BoolVar(&opts.showVersion, "version", false, "Print version information and exit")

	fs.BoolVar(&opts.checkDeps, "check-dependencies", false,
		"Verify prerequisites (curl or wget) for --update, then exit")
	fs.BoolVar(&opts.deploySystemd, "deploy-as-systemd-service", false,
		"Install the exporter as a systemd service (Linux, requires root), then exit")
	fs.BoolVar(&opts.deployWindows, "deploy-as-windows-service", false,
		"Install the exporter as a Windows service (Windows, requires Administrator), then exit")
	fs.BoolVar(&opts.deployService, "deploy-as-service", false,
		"Install the exporter as a system service (systemd on Linux, Windows service on Windows), then exit")
	fs.BoolVar(&opts.uninstall, "uninstall", false,
		"Stop/disable and remove the system service (Linux, requires root; Windows, requires Administrator), then exit")
	fs.BoolVar(&opts.update, "update", false,
		"Update to the latest release .deb via dpkg and restart the service (Linux, requires root), then exit")
	fs.StringVar(&opts.serviceName, "service-name", "gitlab-procs-exporter",
		"Service name (systemd unit name on Linux, service name on Windows)")
	fs.StringVar(&opts.serviceUser, "service-user", defaultServiceUser(goos),
		"User the service runs as (root on Linux, LocalSystem on Windows)")
	fs.BoolVar(&opts.kubeletInsecure, "kubelet-insecure", true,
		"Skip TLS verification when querying the node-local kubelet (in-cluster only)")
	fs.StringVar(&opts.configPath, "config", "",
		"Path to a YAML config file with extra environ redaction rules and per-label size limits (max_label_bytes); an unreadable file or an invalid entry aborts startup (also baked into the service definition when deployed)")

	err := fs.Parse(args)
	return opts, err
}

func resolveAction(goos string, opts *cliOptions) actionType {
	if opts.showVersion {
		return actionVersion
	}
	if opts.checkDeps {
		return actionCheckDeps
	}
	if opts.deployWindows {
		return actionDeployWindows
	}
	if opts.deploySystemd {
		if goos == "windows" {
			return actionDeployWindows
		}
		return actionDeploySystemd
	}
	if opts.deployService {
		if goos == "windows" {
			return actionDeployWindows
		}
		return actionDeploySystemd
	}
	if opts.uninstall {
		if goos == "windows" {
			return actionUninstallWindows
		}
		return actionUninstallSystemd
	}
	if opts.update {
		return actionUpdate
	}
	return actionRun
}

func main() {
	opts, err := parseFlags(flag.CommandLine, os.Args[1:], runtime.GOOS)
	if err != nil {
		log.Fatalf("flag parse error: %v", err)
	}

	switch resolveAction(runtime.GOOS, opts) {
	case actionVersion:
		fmt.Println(version())
		return
	case actionCheckDeps:
		results := deploy.CheckDependencies()
		deploy.PrintResults(os.Stdout, results)
		if !deploy.AllPassed(results) {
			os.Exit(1)
		}
		return
	case actionDeployWindows:
		err := deploy.InstallWindowsService(os.Stdout, deploy.ServiceConfig{
			ServiceName: opts.serviceName,
			ServiceUser: opts.serviceUser,
			Port:        opts.port,
			Interval:    opts.scrapeInterval,
			ConfigPath:  opts.configPath,
		})
		if err != nil {
			log.Fatalf("deploy failed: %v", err)
		}
		return
	case actionDeploySystemd:
		err := deploy.InstallService(os.Stdout, deploy.ServiceConfig{
			ServiceName: opts.serviceName,
			ServiceUser: opts.serviceUser,
			Port:        opts.port,
			Interval:    opts.scrapeInterval,
			ConfigPath:  opts.configPath,
		})
		if err != nil {
			log.Fatalf("deploy failed: %v", err)
		}
		return
	case actionUninstallWindows:
		err := deploy.UninstallWindowsService(os.Stdout, deploy.ServiceConfig{
			ServiceName: opts.serviceName,
		})
		if err != nil {
			log.Fatalf("uninstall failed: %v", err)
		}
		return
	case actionUninstallSystemd:
		err := deploy.UninstallService(os.Stdout, deploy.ServiceConfig{
			ServiceName: opts.serviceName,
		})
		if err != nil {
			log.Fatalf("uninstall failed: %v", err)
		}
		return
	case actionUpdate:
		err := deploy.UpdateService(os.Stdout, deploy.ServiceConfig{
			ServiceName: opts.serviceName,
		})
		if err != nil {
			log.Fatalf("update failed: %v", err)
		}
		return
	case actionRun:
		// continue to server startup below
	}

	if deploy.IsWindowsService() {
		err := deploy.RunWindowsService(opts.serviceName, func(ctx context.Context) error {
			return run(ctx, opts)
		})
		if err != nil {
			log.Fatalf("service execution failed: %v", err)
		}
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := run(ctx, opts); err != nil {
		log.Fatalf("Error starting HTTP server: %v", err)
	}
}

func run(ctx context.Context, opts *cliOptions) error {
	store := exporter.NewHistoryStore()

	inCluster := exporter.InCluster()

	// Start background scraping thread
	go startScraper(ctx, store, opts.scrapeInterval, inCluster)

	// Load optional config for extra environ redaction rules and label-size
	// overrides (fail-fast: a rejected override must not start silently).
	var cfg *exporter.Config
	var redactKeySubstrings []string
	if opts.configPath != "" {
		loaded, err := exporter.LoadConfig(opts.configPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		cfg = loaded
		redactKeySubstrings = cfg.RedactKeySubstrings
	}

	// Register Prometheus custom collector
	collector := exporter.NewProcessCollectorWithConfig(store, cfg)
	prometheus.MustRegister(collector)

	// When running inside Kubernetes, also export per-job pod resource requests.
	if inCluster {
		kubeStore := exporter.NewKubeStore()
		client, err := exporter.NewKubeletClient(opts.kubeletInsecure)
		if err != nil {
			log.Printf("kube: disabled (kubelet client init failed: %v)", err)
		} else {
			go startKubeScraper(ctx, kubeStore, client, opts.scrapeInterval)
			prometheus.MustRegister(exporter.NewKubeCollectorWithConfig(store, kubeStore, cfg, collector))
			log.Printf("kube: enabled, polling kubelet at %s", client.BaseURL()) //nolint:gosec // G706: BaseURL is a fixed in-cluster address
		}
	}

	// Configure HTTP Routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", serveDashboard)
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/api/processes", func(w http.ResponseWriter, r *http.Request) {
		serveAPIProcesses(w, r, store, redactKeySubstrings)
	})
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		serveAPIHistory(w, r, store, redactKeySubstrings)
	})

	log.Printf("Starting GitLab Process History Exporter %s on :%d", version(), opts.port)        //nolint:gosec // G706: port is an operator-supplied flag
	log.Printf("Embedded web dashboard available at http://localhost:%d/", opts.port)             //nolint:gosec // G706: port is an operator-supplied flag
	log.Printf("Prometheus metrics endpoint available at http://localhost:%d/metrics", opts.port) //nolint:gosec // G706: port is an operator-supplied flag

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", opts.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("HTTP server error: %w", err)
		}
		return nil
	}
}

// version reports the build revision. It prefers the ldflags-injected value
// and otherwise falls back to the module version recorded by `go install`.
func version() string {
	if revision != "dev" {
		return revision
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return revision
}

func serveDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	htmlBytes, err := dashboardHTML.ReadFile("dashboard/index.html")
	if err != nil {
		http.Error(w, "Dashboard file not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(htmlBytes)
}

func serveAPIProcesses(w http.ResponseWriter, r *http.Request, store *exporter.HistoryStore, redactKeySubstrings []string) {
	w.Header().Set("Content-Type", "application/json")
	active := store.GetActiveProcesses()
	for i := range active {
		active[i].Environ = exporter.RedactEnviron(active[i].Environ, redactKeySubstrings)
	}
	_ = json.NewEncoder(w).Encode(active)
}

func serveAPIHistory(w http.ResponseWriter, r *http.Request, store *exporter.HistoryStore, redactKeySubstrings []string) {
	w.Header().Set("Content-Type", "application/json")

	pidStr := r.URL.Query().Get("pid")
	nameStr := r.URL.Query().Get("name")

	var history map[string][]exporter.ProcessSample
	if pidStr != "" {
		history = store.QueryHistory("pid", pidStr)
	} else if nameStr != "" {
		history = store.QueryHistory("name", nameStr)
	} else {
		http.Error(w, `{"error": "Missing 'pid' or 'name' query parameter"}`, http.StatusBadRequest)
		return
	}

	// Redact each timeline's environ in place. QueryHistory already returns
	// fresh copies of the sample slices (see exporter.HistoryStore.QueryHistory),
	// so mutating them here never touches the store's own samples.
	for _, samples := range history {
		for i := range samples {
			samples[i].Environ = exporter.RedactEnviron(samples[i].Environ, redactKeySubstrings)
		}
	}

	_ = json.NewEncoder(w).Encode(history)
}

// Background scraper with process object reuse for CPU calculation accuracy
func startScraper(ctx context.Context, store *exporter.HistoryStore, interval time.Duration, inCluster bool) {
	procCache := make(map[int32]*process.Process)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	scrape(store, procCache, inCluster)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scrape(store, procCache, inCluster)
		}
	}
}

// startKubeScraper polls the kubelet for node-local pod resource requests.
func startKubeScraper(ctx context.Context, kubeStore *exporter.KubeStore, client *exporter.KubeletClient, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	update := func() {
		pods, err := client.Pods()
		if err != nil {
			log.Printf("kube: scrape error: %v", err)
			return
		}
		kubeStore.Replace(pods)
	}

	update()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}

// liveProcess returns the cached *process.Process for pid, evicting the entry
// when the PID now belongs to a different process (the kernel reuses PIDs;
// gopsutil's IsRunning compares create times). Without this, the newcomer
// inherits the old process's CPU Percent baseline and cached create time.
func liveProcess(procCache map[int32]*process.Process, pid int32) (*process.Process, error) {
	if p, ok := procCache[pid]; ok {
		if running, err := p.IsRunning(); err == nil && running {
			return p, nil
		}
		delete(procCache, pid)
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return nil, err
	}
	procCache[pid] = p
	return p, nil
}

func scrape(store *exporter.HistoryStore, procCache map[int32]*process.Process, inCluster bool) {
	pids, err := process.Pids()
	if err != nil {
		log.Printf("Error getting PIDs: %v", err)
		return
	}

	activePids := make(map[int32]bool)
	now := time.Now()

	for _, pid := range pids {
		activePids[pid] = true

		p, err := liveProcess(procCache, pid)
		if err != nil {
			continue // Process exited or inaccessible
		}

		// Retrieve fields, handling errors gracefully
		name, err := p.Name()
		if err != nil {
			continue // Process dead
		}

		createTime, err := p.CreateTime()
		if err != nil {
			createTime = now.UnixNano() / int64(time.Millisecond) // fallback
		}

		cmdline, err := p.Cmdline()
		if err != nil {
			cmdline = ""
		}

		// CPU Usage percent
		cpuUsage, err := p.Percent(0)
		if err != nil {
			cpuUsage = 0.0
		}

		// Cumulative CPU seconds (user+system) — feeds the _total counter.
		var cpuSeconds float64
		if times, err := p.Times(); err == nil && times != nil {
			cpuSeconds = times.User + times.System
		}

		// Memory RSS and VMS
		var rss, vms uint64
		memInfo, err := p.MemoryInfo()
		if err == nil && memInfo != nil {
			rss = memInfo.RSS
			vms = memInfo.VMS
		}

		// I/O Counters. IOCounters reads /proc/<pid>/io, which also carries the
		// accounting of every child this process has reaped; SelfIO sums the
		// per-thread files instead and so credits only the issuing process.
		// Both are exported — neither is the whole truth on its own.
		var ioRead, ioWrite, ioReadSyscalls, ioWriteSyscalls uint64
		ioCounters, err := p.IOCounters()
		if err == nil && ioCounters != nil {
			ioRead = ioCounters.ReadBytes
			ioWrite = ioCounters.WriteBytes
			ioReadSyscalls = ioCounters.ReadCount   // /proc syscr
			ioWriteSyscalls = ioCounters.WriteCount // /proc syscw
		}

		var self exporter.IOStats
		if st, err := exporter.SelfIO("/proc", pid); err == nil {
			self = st
		}

		// Environment variables
		environMap := make(map[string]string)
		environSlice, err := p.Environ()
		if err == nil {
			for _, env := range environSlice {
				parts := strings.SplitN(env, "=", 2)
				if len(parts) == 2 {
					environMap[parts[0]] = parts[1]
				}
			}
		}

		// Resolve the owning pod UID from cgroup (in-cluster only).
		podUID := ""
		if inCluster {
			podUID = exporter.ProcessPodUID(pid)
		}

		// Add sample to HistoryStore
		store.AddSample(exporter.ProcessSample{
			Timestamp:           now,
			PID:                 pid,
			Name:                name,
			PodUID:              podUID,
			CmdLine:             cmdline,
			Environ:             environMap,
			CPUUsage:            cpuUsage,
			CPUSeconds:          cpuSeconds,
			MemoryRSS:           rss,
			MemoryVMS:           vms,
			IORead:              ioRead,
			IOWrite:             ioWrite,
			IOReadSyscalls:      ioReadSyscalls,
			IOWriteSyscalls:     ioWriteSyscalls,
			IOReadSelf:          self.ReadBytes,
			IOWriteSelf:         self.WriteBytes,
			IOReadSyscallsSelf:  self.ReadSyscalls,
			IOWriteSyscallsSelf: self.WriteSyscalls,
			CreateTime:          createTime,
			IsActive:            true,
		})
	}

	// Mark inactive and purge from persistent cache
	store.MarkInactive(activePids)
	for pid := range procCache {
		if !activePids[pid] {
			delete(procCache, pid)
		}
	}
}
