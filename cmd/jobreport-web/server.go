package main

import "net/http"

// Server holds the web service's runtime configuration: where the Prometheus URL
// store lives on disk and the path to this binary, which is re-exec'd (self-exec)
// as `report ...` to actually produce a report.
type Server struct {
	storePath string // path to the Prometheus URL store JSON file
	selfPath  string // path to this binary, re-exec'd as the report subcommand
}

// NewServer builds a Server. selfPath should be os.Args[0]; it is the binary that
// gets re-exec'd with a leading "report" argument to run jobreport.
func NewServer(storePath, selfPath string) *Server {
	return &Server{storePath: storePath, selfPath: selfPath}
}

// routes wires the HTTP handlers and returns the mux.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/prometheus", s.handlePrometheus)
	mux.HandleFunc("/report", s.handleReport)
	return mux
}

// handleIndex renders the main page. Stub: returns 200 until Phase F.
func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handlePrometheus adds a Prometheus URL to the store. Stub: returns 200 until Phase F.
func (s *Server) handlePrometheus(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleReport runs a report and returns the output fragment. Stub: returns 200 until Phase F.
func (s *Server) handleReport(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
