package jobreport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// defaultPromURL is the fallback Prometheus base URL used only when neither the
// -prom flag nor the PROMETHEUS_URL environment variable is set. It deliberately
// points at localhost — no site-specific host or IP is baked into the binary.
const defaultPromURL = "http://localhost:9090"

// promResp is the shape of /api/v1/query and /api/v1/query_range responses.
type promResp struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`  // [ts, "val"] (instant)
			Values [][]any           `json:"values"` // [[ts,"val"],...] (range)
		} `json:"result"`
	} `json:"data"`
}

type promClient struct {
	base string
	http *http.Client
}

// newPromClient builds a client against the given Prometheus base URL. An empty
// base falls back to defaultPromURL so callers can pass through unset config. A
// base with no scheme (e.g. "prometheus.example.net", as commonly set via
// $PROMETHEUS_URL) defaults to https, and a trailing slash is trimmed so paths
// join cleanly.
func newPromClient(base string) *promClient {
	if base == "" {
		base = defaultPromURL
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	return &promClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}
}

// Retry policy for transient upstream failures. A Prometheus behind a reverse
// proxy (e.g. nginx) intermittently returns 502/503/504 while it restarts or is
// briefly unreachable; a couple of quick retries turn most of those into a
// successful response instead of surfacing a confusing gateway error.
const maxAttempts = 3

// retryBackoff is the base delay between attempts (attempt N waits N*backoff). It
// is a var so tests can set it to zero.
var retryBackoff = 500 * time.Millisecond

// retryableStatus reports whether an HTTP status is a transient gateway failure
// worth retrying.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (c *promClient) get(path string, q url.Values) (*promResp, error) {
	u := c.base + path + "?" + q.Encode()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		pr, retry, err := c.getOnce(u)
		if err == nil {
			return pr, nil
		}
		lastErr = err
		if !retry || attempt == maxAttempts {
			break
		}
		time.Sleep(retryBackoff * time.Duration(attempt))
	}
	return nil, lastErr
}

// getOnce performs a single GET. The bool reports whether a failure is transient
// (a transport error or a 5xx gateway status) and therefore worth retrying.
func (c *promClient) getOnce(u string) (*promResp, bool, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, err // transport-level failure: retry
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode != 200 {
		return nil, retryableStatus(resp.StatusCode), fmt.Errorf("prometheus HTTP %d: %s", resp.StatusCode, string(body))
	}
	var pr promResp
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, false, fmt.Errorf("prometheus decode: %w", err)
	}
	if pr.Status != "success" {
		return nil, false, fmt.Errorf("prometheus status=%s", pr.Status)
	}
	return &pr, false, nil
}

// queryInstant runs an instant query at time t (unix seconds).
func (c *promClient) queryInstant(expr string, t int64) (*promResp, error) {
	q := url.Values{}
	q.Set("query", expr)
	q.Set("time", strconv.FormatInt(t, 10))
	return c.get("/api/v1/query", q)
}

// queryRange runs a range query.
func (c *promClient) queryRange(expr string, start, end, step int64) (*promResp, error) {
	q := url.Values{}
	q.Set("query", expr)
	q.Set("start", strconv.FormatInt(start, 10))
	q.Set("end", strconv.FormatInt(end, 10))
	q.Set("step", strconv.FormatInt(step, 10))
	return c.get("/api/v1/query_range", q)
}

// instantFloat extracts the float value of an instant-query series.
func instantFloat(v []any) (float64, bool) {
	if len(v) != 2 {
		return 0, false
	}
	s, ok := v[1].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// selector builds a PromQL label matcher `{...}` from proc/node ("all" => omit).
func selector(proc, node string) string {
	sel := ""
	if proc != "all" {
		sel = fmt.Sprintf("name=%q", proc)
	}
	if node != "all" {
		if sel != "" {
			sel += ","
		}
		sel += fmt.Sprintf("node_ip=%q", node)
	}
	return "{" + sel + "}"
}

// metaMap fetches gitlab_process_info over the window and returns node_ip|pid -> ProcMeta,
// keeping the richest (longest-cmdline) series per pid.
func (c *promClient) metaMap(F string, start, end, step int64) (map[string]ProcMeta, error) {
	pr, err := c.queryRange("gitlab_process_info"+F, start, end, step)
	if err != nil {
		return nil, err
	}
	best := map[string]string{} // key -> cmdline len marker not needed; store via map of ProcMeta
	out := map[string]ProcMeta{}
	for _, s := range pr.Data.Result {
		key := s.Metric["node_ip"] + "|" + s.Metric["pid"]
		cmd := s.Metric["cmdline"]
		if cur, ok := best[key]; ok && len(cmd) <= len(cur) {
			continue
		}
		best[key] = cmd
		out[key] = extractMeta(cmd, s.Metric["environ"])
	}
	return out, nil
}

// topRow is one ranked process row with its resolved metadata.
type topRow struct {
	Name string
	PID  string
	Node string
	Val  float64
	Meta ProcMeta
}

// topN runs `topk(n, expr)` at time=end and joins each series with meta.
func (c *promClient) topN(expr string, n int, end int64, meta map[string]ProcMeta) ([]topRow, error) {
	pr, err := c.queryInstant(fmt.Sprintf("topk(%d, %s)", n, expr), end)
	if err != nil {
		return nil, err
	}
	rows := make([]topRow, 0, len(pr.Data.Result))
	for _, s := range pr.Data.Result {
		v, ok := instantFloat(s.Value)
		if !ok {
			continue
		}
		node, pid := s.Metric["node_ip"], s.Metric["pid"]
		rows = append(rows, topRow{
			Name: s.Metric["name"], PID: pid, Node: node, Val: v,
			Meta: meta[node+"|"+pid],
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Val > rows[j].Val })
	return rows, nil
}

// resolveNodeByJobID finds node_ip(s) whose processes carried this JOB_ID in the window.
// Returns nil if none (e.g. k8s-executor jobs that aren't scraped).
func (c *promClient) resolveNodeByJobID(jobID string, start, end, step int64) ([]string, error) {
	if jobID == "" {
		return nil, nil
	}
	// The GitLab job id is embedded in the process *cmdline* as "JOB_ID=<id>"
	// (shell/docker executors that run on scraped VM nodes); environ does not
	// carry it. Some setups instead expose it in the environment as
	// "CI_JOB_ID=<id>", so match either. The trailing "([^0-9].*)?" is an
	// edge/non-digit boundary so JOB_ID=123 doesn't also match JOB_ID=1234.
	bound := jobID + "([^0-9].*)?"
	expr := fmt.Sprintf(
		"count by (node_ip)(gitlab_process_info{cmdline=~%q} or gitlab_process_info{environ=~%q})",
		".*JOB_ID="+bound, ".*CI_JOB_ID="+bound,
	)
	pr, err := c.queryRange(expr, start, end, step)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var nodes []string
	for _, s := range pr.Data.Result {
		if ip := s.Metric["node_ip"]; ip != "" && !seen[ip] {
			seen[ip] = true
			nodes = append(nodes, ip)
		}
	}
	sort.Strings(nodes)
	return nodes, nil
}
