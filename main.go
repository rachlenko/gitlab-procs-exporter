package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
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

func main() {
	port := flag.Int("port", 8000, "Port to run the exporter on")
	scrapeInterval := flag.Duration("interval", 10*time.Second, "Scrape interval")
	showVersion := flag.Bool("version", false, "Print version information and exit")

	checkDeps := flag.Bool("check-dependencies", false,
		"Verify prerequisites (curl or wget) for --update, then exit")
	deploySystemd := flag.Bool("deploy-as-systemd-service", false,
		"Install the exporter as a systemd service (Linux, requires root), then exit")
	uninstall := flag.Bool("uninstall", false,
		"Stop/disable the systemd service and remove its unit file (Linux, requires root), then exit")
	update := flag.Bool("update", false,
		"Update to the latest release .deb via dpkg and restart the service (Linux, requires root), then exit")
	serviceName := flag.String("service-name", "gitlab-procs-exporter",
		"systemd unit name")
	serviceUser := flag.String("service-user", "root",
		"User the service runs as (root is required to read all processes' env/IO)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version())
		return
	}

	if *checkDeps {
		results := deploy.CheckDependencies()
		deploy.PrintResults(os.Stdout, results)
		if !deploy.AllPassed(results) {
			os.Exit(1)
		}
		return
	}

	if *deploySystemd {
		err := deploy.InstallService(os.Stdout, deploy.ServiceConfig{
			ServiceName: *serviceName,
			ServiceUser: *serviceUser,
			Port:        *port,
			Interval:    *scrapeInterval,
		})
		if err != nil {
			log.Fatalf("deploy failed: %v", err)
		}
		return
	}

	if *uninstall {
		err := deploy.UninstallService(os.Stdout, deploy.ServiceConfig{
			ServiceName: *serviceName,
		})
		if err != nil {
			log.Fatalf("uninstall failed: %v", err)
		}
		return
	}

	if *update {
		err := deploy.UpdateService(os.Stdout, deploy.ServiceConfig{
			ServiceName: *serviceName,
		})
		if err != nil {
			log.Fatalf("update failed: %v", err)
		}
		return
	}

	store := exporter.NewHistoryStore()

	// Start background scraping thread
	go startScraper(store, *scrapeInterval)

	// Register Prometheus custom collector
	collector := exporter.NewProcessCollector(store)
	prometheus.MustRegister(collector)

	// Configure HTTP Routes
	http.HandleFunc("/", serveDashboard)
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/api/processes", func(w http.ResponseWriter, r *http.Request) {
		serveAPIProcesses(w, r, store)
	})
	http.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		serveAPIHistory(w, r, store)
	})

	log.Printf("Starting GitLab Process History Exporter %s on :%d", version(), *port)
	log.Printf("Embedded web dashboard available at http://localhost:%d/", *port)
	log.Printf("Prometheus metrics endpoint available at http://localhost:%d/metrics", *port)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalf("Error starting HTTP server: %v", err)
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
	w.Write(htmlBytes)
}

func serveAPIProcesses(w http.ResponseWriter, r *http.Request, store *exporter.HistoryStore) {
	w.Header().Set("Content-Type", "application/json")
	active := store.GetActiveProcesses()
	json.NewEncoder(w).Encode(active)
}

func serveAPIHistory(w http.ResponseWriter, r *http.Request, store *exporter.HistoryStore) {
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

	json.NewEncoder(w).Encode(history)
}

// Background scraper with process object reuse for CPU calculation accuracy
func startScraper(store *exporter.HistoryStore, interval time.Duration) {
	// Keep track of active *process.Process objects to maintain CPU delta calculations
	procCache := make(map[int32]*process.Process)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run initial scrape immediately
	scrape(store, procCache)

	for range ticker.C {
		scrape(store, procCache)
	}
}

func scrape(store *exporter.HistoryStore, procCache map[int32]*process.Process) {
	pids, err := process.Pids()
	if err != nil {
		log.Printf("Error getting PIDs: %v", err)
		return
	}

	activePids := make(map[int32]bool)
	now := time.Now()

	for _, pid := range pids {
		activePids[pid] = true

		// Check if we already have a persistent Process object
		p, exists := procCache[pid]
		if !exists {
			var err error
			p, err = process.NewProcess(pid)
			if err != nil {
				continue // Process exited or inaccessible
			}
			procCache[pid] = p
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

		// Memory RSS and VMS
		var rss, vms uint64
		memInfo, err := p.MemoryInfo()
		if err == nil && memInfo != nil {
			rss = memInfo.RSS
			vms = memInfo.VMS
		}

		// I/O Counters
		var ioRead, ioWrite uint64
		ioCounters, err := p.IOCounters()
		if err == nil && ioCounters != nil {
			ioRead = ioCounters.ReadBytes
			ioWrite = ioCounters.WriteBytes
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

		// Add sample to HistoryStore
		store.AddSample(exporter.ProcessSample{
			Timestamp:  now,
			PID:        pid,
			Name:       name,
			CmdLine:    cmdline,
			Environ:    environMap,
			CPUUsage:   cpuUsage,
			MemoryRSS:  rss,
			MemoryVMS:  vms,
			IORead:     ioRead,
			IOWrite:    ioWrite,
			CreateTime: createTime,
			IsActive:   true,
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
