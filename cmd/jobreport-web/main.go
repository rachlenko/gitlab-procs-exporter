// Command jobreport-web serves an htmx UI for running jobreport against a chosen
// Prometheus URL, job id, and time window, showing the raw report output.
//
// It is a single self-contained binary: all HTML templates, htmx.js, and CSS are
// embedded, and the report itself is produced by this SAME binary re-exec'ing
// itself as a "report" subcommand (self-exec). That keeps one runtime artifact:
// when invoked as `jobreport-web report ...` the process behaves exactly as the
// jobreport CLI; otherwise it starts the web server.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/rachlenko/gitlab-procs-exporter/internal/jobreport"
)

func main() {
	// Self-exec dispatch: when the first argument is "report", act AS jobreport.
	// The web server re-execs itself this way to produce a report, so there is a
	// single binary on disk.
	if len(os.Args) > 1 && os.Args[1] == "report" {
		os.Exit(jobreport.Main(os.Args[2:]))
	}

	if err := serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// serve parses the web flags (with environment-variable fallbacks) and starts the
// HTTP server. It returns an error only on a fatal startup/serve failure.
func serve(argv []string) error {
	fs := flag.NewFlagSet("jobreport-web", flag.ContinueOnError)
	addr := fs.String("addr", envOr("JOBREPORT_WEB_ADDR", ":8088"),
		"HTTP listen address (env JOBREPORT_WEB_ADDR)")
	store := fs.String("store", envOr("JOBREPORT_WEB_STORE", "./jobreport-web-urls.json"),
		"path to the Prometheus URL store JSON file (env JOBREPORT_WEB_STORE)")
	debug := fs.Bool("debug", os.Getenv("JOBREPORT_WEB_DEBUG") != "",
		"verbose debug logging: response sizes, remote addrs, report output sizes (env JOBREPORT_WEB_DEBUG)")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	srv := NewServer(*store, selfPath())
	srv.debug = *debug
	//nolint:gosec // G706: addr/store are operator-supplied flags, not request data
	log.Printf("jobreport-web listening on %s (store %s, debug=%t)", *addr, *store, *debug)
	if *debug {
		log.Printf("[debug] verbose logging enabled")
	}
	return http.ListenAndServe(*addr, srv.Handler()) //nolint:gosec // G114: internal tool, no timeouts needed; see README SSRF caveat
}

// selfPath returns the absolute path to this executable, used to re-exec the
// report subcommand. It prefers os.Executable() (absolute and immune to cwd or
// $PATH changes after startup) and falls back to os.Args[0] only if that fails.
func selfPath() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return os.Args[0]
}

// envOr returns $key if set and non-empty, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
