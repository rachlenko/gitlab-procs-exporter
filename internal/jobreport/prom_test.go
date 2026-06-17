package jobreport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewPromClientBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "http://localhost:9090"},                                         // default
		{"prometheus.example.net", "https://prometheus.example.net"},          // no scheme -> https
		{"prometheus.example.net/", "https://prometheus.example.net"},         // trailing slash trimmed
		{"https://prometheus.example.net/", "https://prometheus.example.net"}, // already has scheme
		{"http://localhost:9090", "http://localhost:9090"},                    // http preserved
	}
	for _, c := range cases {
		if got := newPromClient(c.in).base; got != c.want {
			t.Errorf("newPromClient(%q).base = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSelector(t *testing.T) {
	cases := []struct{ proc, node, want string }{
		{"all", "all", "{}"},
		{"java", "all", `{name="java"}`},
		{"all", "10.0.1.11", `{node_ip="10.0.1.11"}`},
		{"java", "10.0.1.11", `{name="java",node_ip="10.0.1.11"}`},
	}
	for _, c := range cases {
		if got := selector(c.proc, c.node); got != c.want {
			t.Errorf("selector(%q,%q) = %q, want %q", c.proc, c.node, got, c.want)
		}
	}
}

func TestTopNJoinAndSort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// instant topk response, deliberately out of order
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"name":"AppFirewallCore","pid":"23653","node_ip":"10.0.1.11"},"value":[1780259356,"3.87"]},
			{"metric":{"name":"java","pid":"19991","node_ip":"10.0.1.11"},"value":[1780259356,"5.92"]}
		]}}`))
	}))
	defer srv.Close()
	c := &promClient{base: srv.URL, http: srv.Client()}

	meta := map[string]ProcMeta{
		"10.0.1.11|19991": {Shard: "glrtr-eS/0", JobID: "126739895"},
	}
	rows, err := c.topN("ignored", 5, 1780259356, meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].PID != "19991" || rows[0].Val != 5.92 {
		t.Errorf("not sorted desc: %+v", rows[0])
	}
	if rows[0].Meta.Shard != "glrtr-eS/0" {
		t.Errorf("meta not joined: %+v", rows[0].Meta)
	}
}

func TestMetaMapKeepsRichest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// two series for the same pid: one with cmdline, one without (range query)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"node_ip":"10.0.1.11","pid":"22285"},"values":[[1,"1"]]},
			{"metric":{"node_ip":"10.0.1.11","pid":"22285","cmdline":"/builds/glrtr-gW/0/AppBackendService JOB_ID=126741069","environ":"CI_JOB_NAME_SLUG=oracle-xml"},"values":[[1,"1"]]}
		]}}`))
	}))
	defer srv.Close()
	c := &promClient{base: srv.URL, http: srv.Client()}
	m, err := c.metaMap("{}", 1, 2, 15)
	if err != nil {
		t.Fatal(err)
	}
	got := m["10.0.1.11|22285"]
	if got.JobID != "126741069" || got.Shard != "glrtr-gW/0" {
		t.Errorf("did not keep richest series: %+v", got)
	}
}

func TestResolveNodeByJobID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The job id is carried in the cmdline label as "JOB_ID=<id>" (it is NOT
		// in environ), so the resolver must match on cmdline, not environ.
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, `cmdline=~`) || !strings.Contains(query, "JOB_ID=126741069") {
			t.Errorf("resolver must match cmdline JOB_ID; got query: %s", query)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"node_ip":"10.0.1.11"},"values":[[1,"1"]]}
		]}}`))
	}))
	defer srv.Close()
	c := &promClient{base: srv.URL, http: srv.Client()}
	nodes, err := c.resolveNodeByJobID("126741069", 1, 2, 15)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0] != "10.0.1.11" {
		t.Errorf("nodes = %v", nodes)
	}
}
