package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const sep = "================================================================================"

// config is one run's fully-resolved inputs. Every field comes from a flag with
// an environment-variable fallback, so jobreport drops straight into a GitLab CI
// job (where the values live in the job environment) with no code changes.
type config struct {
	promURL string // Prometheus base URL                (-prom    / $PROMETHEUS_URL)
	proc    string // process-name filter, "all" = none  (-proc    / $JOBREPORT_PROC)
	node    string // node_ip filter, "all" = none        (-node    / $JOBREPORT_NODE)
	window  string // 10m|1h|2d or <start>..<end> epochs  (-window  / $JOBREPORT_WINDOW)
	topN    int    // rows per table                      (-top     / $JOBREPORT_TOP)
	jobURL  string // job.log artifact URL (URL mode)     (-url     / $JOBREPORT_URL)
	jobID   string // GitLab job id, for node resolution  (-job-id  / $CI_JOB_ID)
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err == flag.ErrHelp {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// parseFlags resolves the flag set against argv, applying environment-variable
// defaults. A bare URL given as the first positional argument is accepted as a
// shorthand for -url. The time window is optional: when unset it defaults to the
// last 10 minutes (now-10m .. now).
func parseFlags(argv []string) (config, error) {
	fs := flag.NewFlagSet("jobreport", flag.ContinueOnError)
	var cfg config
	fs.StringVar(&cfg.promURL, "prom", envOr("PROMETHEUS_URL", defaultPromURL),
		"Prometheus base URL (env PROMETHEUS_URL)")
	fs.StringVar(&cfg.proc, "proc", envOr("JOBREPORT_PROC", "all"),
		`process-name filter, or "all" for no filter (env JOBREPORT_PROC)`)
	fs.StringVar(&cfg.node, "node", envOr("JOBREPORT_NODE", "all"),
		`node_ip filter, or "all" for no filter (env JOBREPORT_NODE)`)
	fs.StringVar(&cfg.window, "window", envOr("JOBREPORT_WINDOW", "10m"),
		"time window: 10m|1h|2d (relative) or <startEpoch>..<endEpoch> (absolute UTC); "+
			"optional — defaults to the last 10 minutes (env JOBREPORT_WINDOW)")
	fs.IntVar(&cfg.topN, "top", envInt("JOBREPORT_TOP", 5),
		"number of rows per table (env JOBREPORT_TOP)")
	fs.StringVar(&cfg.jobURL, "url", os.Getenv("JOBREPORT_URL"),
		"GitLab job.log artifact URL; enables URL mode (env JOBREPORT_URL)")
	fs.StringVar(&cfg.jobID, "job-id", os.Getenv("CI_JOB_ID"),
		"GitLab job id; auto-resolves the runner node from gitlab_process_info (env CI_JOB_ID)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "jobreport — gitlab-procs-exporter top-N process report (CPU / peak-RSS / IO)")
		fmt.Fprintln(fs.Output(), "\nUsage:")
		fmt.Fprintln(fs.Output(), "  jobreport [flags]")
		fmt.Fprintln(fs.Output(), "  jobreport <job.log-URL> [flags]   # URL mode: parse the log, scope to its window/node")
		fmt.Fprintln(fs.Output(), "\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return cfg, err
	}
	// Accept a bare URL as the first positional argument (shorthand for -url).
	if cfg.jobURL == "" {
		for _, a := range fs.Args() {
			if strings.HasPrefix(a, "http") {
				cfg.jobURL = a
				break
			}
		}
	}
	return cfg, nil
}

// run dispatches to URL mode (window derived from the parsed log) or to the
// plain top-N report scoped to the configured window/node.
func run(cfg config) error {
	if cfg.jobURL != "" {
		return runURL(cfg.promURL, cfg.jobURL, cfg.topN)
	}

	start, end, err := parseWindow(cfg.window)
	if err != nil {
		return err
	}
	c := newPromClient(cfg.promURL)

	// When a CI job id is supplied and the node wasn't pinned explicitly, resolve
	// the runner node automatically from the metrics for that job over the window.
	node := cfg.node
	nodeNote := ""
	if cfg.jobID != "" && node == "all" {
		nodes, rerr := c.resolveNodeByJobID(cfg.jobID, start, end, stepFor(start, end))
		if rerr != nil {
			return rerr
		}
		switch {
		case len(nodes) == 1:
			node, nodeNote = nodes[0], "resolved from CI_JOB_ID via gitlab_process_info"
		case len(nodes) > 1:
			nodeNote = "CI_JOB_ID seen on multiple nodes: " + strings.Join(nodes, ", ") + " (showing all)"
		default:
			nodeNote = "CI_JOB_ID not found in gitlab_process_info (node not scraped — e.g. k8s executor); showing all nodes"
		}
	}

	scope := fmt.Sprintf("proc=%s  node=%s", cfg.proc, node)
	if cfg.jobID != "" {
		scope += "  job=" + cfg.jobID
	}
	fmt.Println(sep)
	fmt.Printf(" gitlab-procs-exporter — top-%d process report   |   %s\n", cfg.topN, scope)
	fmt.Printf(" window: %s -> %s UTC   (%ds)\n", fdate(start), fdate(end), end-start)
	if nodeNote != "" {
		fmt.Printf(" node scope: %s\n", nodeNote)
	}
	fmt.Println(sep)
	return report(c, cfg.proc, node, start, end, cfg.topN)
}

// envOr returns $key if set and non-empty, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt returns $key parsed as an int if set and valid, else def.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// parseWindow accepts `10m|1h|2d` (relative to now) or `<startEpoch>..<endEpoch>`.
func parseWindow(win string) (int64, int64, error) {
	if i := strings.Index(win, ".."); i >= 0 {
		s, err1 := strconv.ParseInt(win[:i], 10, 64)
		e, err2 := strconv.ParseInt(win[i+2:], 10, 64)
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("bad absolute window %q", win)
		}
		return s, e, nil
	}
	if len(win) < 2 {
		return 0, 0, fmt.Errorf("bad window %q", win)
	}
	n, err := strconv.ParseInt(win[:len(win)-1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad window %q", win)
	}
	var mult int64
	switch win[len(win)-1] {
	case 's':
		mult = 1
	case 'm':
		mult = 60
	case 'h':
		mult = 3600
	case 'd':
		mult = 86400
	default:
		return 0, 0, fmt.Errorf("bad window unit in %q", win)
	}
	end := time.Now().Unix()
	return end - n*mult, end, nil
}

func stepFor(start, end int64) int64 {
	step := (end - start) / 30
	if step < 15 {
		step = 15
	}
	return step
}

func fdate(t int64) string { return time.Unix(t, 0).UTC().Format("2006-01-02 15:04:05") }

// report fetches metadata + the three top-N tables and prints them.
func report(c *promClient, proc, node string, start, end int64, topN int) error {
	F := selector(proc, node)
	W := end - start
	step := stepFor(start, end)

	cnt, err := c.queryInstant("count(gitlab_process_resident_memory_bytes"+F+")", end)
	if err != nil {
		return err
	}
	if len(cnt.Data.Result) == 0 {
		fmt.Println("\nNO DATA for this scope/window.")
		return nil
	}

	meta, err := c.metaMap(F, start, end, step)
	if err != nil {
		return err
	}

	// 1) CPU
	cpu, err := c.topN(fmt.Sprintf("increase(gitlab_process_cpu_seconds_total%s[%ds])", F, W), topN, end, meta)
	if err != nil {
		return err
	}
	fmt.Println("\nBy CPU (cpu-seconds consumed):")
	fmt.Print(renderRows(cpu, "cmdline / job", func(r topRow) string {
		pct := r.Val / float64(W) * 100
		return fmt.Sprintf("%s s (~%s%%)", num(r.Val, 2), num(pct, 1))
	}, "CPU", 70))

	// 2) Memory (peak RSS)
	mem, err := c.topN(fmt.Sprintf("max_over_time(gitlab_process_resident_memory_bytes%s[%ds])", F, W), topN, end, meta)
	if err != nil {
		return err
	}
	fmt.Println("\nBy memory (peak RSS):")
	fmt.Print(renderRows(mem, "job", func(r topRow) string {
		return num(r.Val/1048576, 1) + " MiB"
	}, "peak RSS", 70))

	// 3) IO (read+write)
	io, err := c.topN(fmt.Sprintf("increase(gitlab_process_io_read_bytes_total%s[%ds]) + increase(gitlab_process_io_write_bytes_total%s[%ds])", F, W, F, W), topN, end, meta)
	if err != nil {
		return err
	}
	fmt.Println("\nBy I/O (total read+write):")
	fmt.Print(renderRows(io, "cmdline / job", func(r topRow) string {
		return num(r.Val/1048576, 0) + " MiB"
	}, "total IO", 52))
	return nil
}

// renderRows builds a top-N table: #, proc, pid, <metricHeader>, <infoHeader>.
func renderRows(rows []topRow, infoHeader string, valStr func(topRow) string, metricHeader string, maxw int) string {
	out := [][]string{{"#", "proc", "pid", metricHeader, infoHeader}}
	for i, r := range rows {
		info := r.Meta.jobCol()
		if info == "" {
			info = firstTok(r.Meta.Cmdline)
		}
		out = append(out, []string{
			strconv.Itoa(i + 1), r.Name, r.PID, valStr(r), info,
		})
	}
	return renderTable(out, maxw)
}
