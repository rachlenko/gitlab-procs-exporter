package main

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server holds the web service's runtime configuration: where the Prometheus URL
// store lives on disk and the path to this binary, which is re-exec'd (self-exec)
// as `report ...` to actually produce a report.
type Server struct {
	storePath string          // path to the Prometheus URL store JSON file
	selfPath  string          // path to this binary, re-exec'd as the report subcommand
	store     PrometheusStore // persistence for the Prometheus URL list
	storeMu   sync.Mutex      // serializes store mutations (read-modify-write in Add)
	debug     bool            // when true, emit verbose [debug] logging
}

// dbg logs a verbose [debug] line only when debug mode is enabled.
func (s *Server) dbg(format string, args ...any) {
	if s.debug {
		//nolint:gosec // G706: callers pass a constant format; tainted values use %q which escapes control chars
		log.Printf("[debug] "+format, args...)
	}
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
	mux.HandleFunc("/prometheus/delete", s.handlePrometheusDelete)
	mux.HandleFunc("/report", s.handleReport)
	return mux
}

// Handler returns the routes wrapped in request logging, for the live server.
// Tests use routes() directly to keep their output quiet.
func (s *Server) Handler() http.Handler {
	return s.logRequests(s.routes())
}

// statusRecorder captures the response status code and byte count for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// logRequests logs one line per request: method, path, status, and duration. In
// debug mode it also logs the response size and the client's remote address.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		dur := time.Since(start).Round(time.Millisecond)
		//nolint:gosec // G706: path/remote logged with %q, which escapes any control characters
		log.Printf("%s %q -> %d (%s)", r.Method, r.URL.Path, rec.status, dur)
		s.dbg("%s %q from %q: %d bytes in %s", r.Method, r.URL.Path, r.RemoteAddr, rec.bytes, dur)
	})
}

// secureHTML sets the response content type to HTML and adds a conservative
// nosniff header before any body is written. It also marks the response
// no-store so a browser never submits a stale cached form (which would post an
// out-of-date layout, e.g. an empty Prometheus field after a UI change).
func secureHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
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
	// Serialize the store's read-modify-write so concurrent adds can't lose an
	// update (the atomic rename prevents a corrupt file, not a lost update).
	s.storeMu.Lock()
	urls, err := s.store.Add(s.storePath, r.FormValue("prom"))
	s.storeMu.Unlock()
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

// handlePrometheusDelete removes the posted URL from the store and returns the
// refreshed urls fragment so the dropdown updates in place.
func (s *Server) handlePrometheusDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		secureHTML(w)
		_ = renderError(w, "could not read form")
		return
	}
	s.storeMu.Lock()
	urls, err := s.store.Remove(s.storePath, r.FormValue("prom"))
	s.storeMu.Unlock()
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

	// The Prometheus URL is a single combobox field (typed or picked from the saved
	// list). A non-empty value is persisted best-effort so it joins the saved list
	// next time; an invalid one is surfaced by runReport's validation.
	prom := strings.TrimSpace(r.FormValue("prom"))
	if prom != "" {
		s.storeMu.Lock()
		if _, err := s.store.Add(s.storePath, prom); err != nil {
			s.dbg("persist prometheus url %q: %v", prom, err)
		}
		s.storeMu.Unlock()
	}

	//nolint:gosec // G706: values logged with %q, which escapes any control characters
	log.Printf("report request: prom=%q job_id=%q window=%q", prom, r.FormValue("job_id"), window)
	output, runErr := runReport(s.selfPath, prom, r.FormValue("job_id"), window)
	s.dbg("report result: %d bytes output, exec err=%v", len(output), runErr)
	if runErr != nil && output == "" {
		// Validation failed before exec (e.g. missing prom URL, bad job id): no
		// captured output to show, so surface the error text itself.
		secureHTML(w)
		_ = renderError(w, runErr.Error())
		return
	}
	if runErr != nil {
		// Non-zero exit with captured output (e.g. Prometheus error, timeout
		// kill with partial output). The output is shown to the user, but log
		// the exec error so operators can see failures that the rendered text
		// alone may not make obvious.
		log.Printf("report exec: %v", runErr)
	}

	secureHTML(w)
	if err := renderReport(w, output); err != nil {
		log.Printf("render report: %v", err)
	}
}
