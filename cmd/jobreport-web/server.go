package main

import (
	"log"
	"net/http"
)

// Server holds the web service's runtime configuration: where the Prometheus URL
// store lives on disk and the path to this binary, which is re-exec'd (self-exec)
// as `report ...` to actually produce a report.
type Server struct {
	storePath string          // path to the Prometheus URL store JSON file
	selfPath  string          // path to this binary, re-exec'd as the report subcommand
	store     PrometheusStore // persistence for the Prometheus URL list
}

// NewServer builds a Server. selfPath should be os.Args[0]; it is the binary that
// gets re-exec'd with a leading "report" argument to run jobreport.
func NewServer(storePath, selfPath string) *Server {
	return &Server{storePath: storePath, selfPath: selfPath, store: PrometheusStore{}}
}

// routes wires the HTTP handlers and returns the mux.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("/prometheus", s.handlePrometheus)
	mux.HandleFunc("/report", s.handleReport)
	return mux
}

// secureHTML sets the response content type to HTML and adds a conservative
// nosniff header before any body is written.
func secureHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// handleIndex renders the main page with the stored Prometheus URLs. Only the root
// path is served here; anything else under "/" is a 404.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	urls, err := s.store.Load(s.storePath)
	if err != nil {
		log.Printf("load store: %v", err)
		urls = []string{} // degrade gracefully: show the page with an empty dropdown
	}
	secureHTML(w)
	if err := renderIndex(w, urls); err != nil {
		log.Printf("render index: %v", err)
	}
}

// handlePrometheus adds the posted URL to the store and returns the refreshed urls
// fragment. An invalid URL yields an error fragment (HTTP 200) so htmx shows it.
func (s *Server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		secureHTML(w)
		_ = renderError(w, "could not read form")
		return
	}
	urls, err := s.store.Add(s.storePath, r.FormValue("url"))
	if err != nil {
		secureHTML(w)
		_ = renderError(w, err.Error())
		return
	}
	secureHTML(w)
	if err := renderURLs(w, urls); err != nil {
		log.Printf("render urls: %v", err)
	}
}

// handleReport builds the (optional) UTC window from the six time fields, runs the
// report via self-exec, and returns the output as a <pre> fragment. Validation
// errors return an error fragment (HTTP 200) rather than a 500 so the user sees
// the message in place.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		secureHTML(w)
		_ = renderError(w, "could not read form")
		return
	}

	window, err := buildWindow(
		r.FormValue("start_date"), r.FormValue("start_hour"), r.FormValue("start_min"),
		r.FormValue("end_date"), r.FormValue("end_hour"), r.FormValue("end_min"),
	)
	if err != nil {
		secureHTML(w)
		_ = renderError(w, err.Error())
		return
	}

	output, runErr := runReport(s.selfPath, r.FormValue("prom"), r.FormValue("job_id"), window)
	if runErr != nil && output == "" {
		// Validation failed before exec (e.g. missing prom URL, bad job id): no
		// captured output to show, so surface the error text itself.
		secureHTML(w)
		_ = renderError(w, runErr.Error())
		return
	}

	secureHTML(w)
	if err := renderReport(w, output); err != nil {
		log.Printf("render report: %v", err)
	}
}
