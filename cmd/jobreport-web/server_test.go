package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer builds a Server backed by a temp store file and a fake self binary
// (a shell script that echoes its args), so /report exercises the real self-exec
// path without needing a Prometheus.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "urls.json")
	return NewServer(storePath, writeFakeSelf(t)), storePath
}

func TestHandleIndex_OK(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`<form`, `/static/htmx.min.js`, `id="prom-list"`, `hx-post="/report"`, `hx-post="/prometheus"`, `id="report-status"`} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /: body missing %q", want)
		}
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("GET /: X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("GET /: Cache-Control = %q, want no-store (avoid stale-form submits)", got)
	}
}

func TestHandleStatic_ServesHTMX(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/static/htmx.min.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/htmx.min.js: status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "htmx") {
		t.Errorf("GET /static/htmx.min.js: body does not look like htmx")
	}
}

func TestHandlePrometheus_PersistsAndReturnsOption(t *testing.T) {
	srv, storePath := newTestServer(t)
	form := url.Values{"url": {"https://prom.example/"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/prometheus", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /prometheus: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="https://prom.example/"`) {
		t.Errorf("POST /prometheus: response missing the new radio option, got %q", body)
	}

	// The URL must have been persisted to the store file.
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if !strings.Contains(string(data), "https://prom.example/") {
		t.Errorf("POST /prometheus: store file does not contain the URL, got %q", data)
	}
}

func TestHandlePrometheus_InvalidURL_ErrorFragment(t *testing.T) {
	srv, _ := newTestServer(t)
	form := url.Values{"url": {"ftp://not-allowed/"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/prometheus", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /prometheus invalid: status = %d, want 200 (error fragment, not 5xx)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Errorf("POST /prometheus invalid: want an error fragment, got %q", rec.Body.String())
	}
}

func TestHandleReport_ReturnsPreWithOutput(t *testing.T) {
	srv, _ := newTestServer(t)
	form := url.Values{"prom": {"https://prom.test/"}, "job_id": {"123"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/report", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /report: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<pre>") {
		t.Fatalf("POST /report: response missing <pre>, got %q", body)
	}
	// The fake self echoes its args; the report invocation must include them.
	for _, want := range []string{"report", "-prom", "https://prom.test/", "-job-id", "123"} {
		if !strings.Contains(body, want) {
			t.Errorf("POST /report: <pre> missing arg %q, got %q", want, body)
		}
	}
}

func TestHandleReport_MissingProm_ErrorFragment(t *testing.T) {
	srv, _ := newTestServer(t)
	// No prom field: runReport fails validation before exec, returning an error
	// with empty output, which must surface as an error fragment (HTTP 200).
	form := url.Values{"job_id": {"123"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/report", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /report missing prom: status = %d, want 200 (error fragment)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="error"`) {
		t.Errorf("POST /report missing prom: want an error fragment, got %q", body)
	}
	if strings.Contains(body, "<pre>") {
		t.Errorf("POST /report missing prom: should not render a report <pre>, got %q", body)
	}
}

func TestHandlePrometheusDelete_RemovesAndReturnsFragment(t *testing.T) {
	srv, storePath := newTestServer(t)
	for _, u := range []string{"https://keep.example/", "https://drop.example/"} {
		form := url.Values{"url": {u}}
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/prometheus", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		srv.routes().ServeHTTP(httptest.NewRecorder(), req)
	}

	form := url.Values{"prom": {"https://drop.example/"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/prometheus/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /prometheus/delete: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "https://drop.example/") {
		t.Errorf("delete: fragment still contains the removed URL: %q", body)
	}
	if !strings.Contains(body, "https://keep.example/") {
		t.Errorf("delete: fragment dropped the kept URL: %q", body)
	}
	data, _ := os.ReadFile(storePath)
	if strings.Contains(string(data), "https://drop.example/") {
		t.Errorf("delete: store still contains removed URL: %q", data)
	}
}

func TestHandleReport_UsesSelectedURL(t *testing.T) {
	srv, _ := newTestServer(t)
	// The selected radio supplies prom; the report must run against it.
	form := url.Values{"prom": {"https://selected.example/"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/report", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /report: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<pre>") || !strings.Contains(body, "https://selected.example/") {
		t.Errorf("POST /report: want a report referencing the selected URL, got %q", body)
	}
}

func TestHandleReport_NoSelection_ErrorFragment(t *testing.T) {
	srv, _ := newTestServer(t)
	// No prom selected: must return a clear "select a URL" error fragment, not run.
	form := url.Values{"job_id": {"123"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/report", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /report no selection: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="error"`) || !strings.Contains(body, "select") {
		t.Errorf("POST /report no selection: want a 'select a URL' error fragment, got %q", body)
	}
	if strings.Contains(body, "<pre>") {
		t.Errorf("POST /report no selection: should not run a report, got %q", body)
	}
}

func TestHandleReport_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/report", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /report: status = %d, want 405", rec.Code)
	}
}

func TestHandlePrometheus_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/prometheus", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /prometheus: status = %d, want 405", rec.Code)
	}
}

func TestHandleIndex_UnknownPath_404(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /does-not-exist: status = %d, want 404", rec.Code)
	}
}

func TestHandleReport_WindowError_ErrorFragmentNot500(t *testing.T) {
	srv, _ := newTestServer(t)
	// Partial window (start date only) is invalid → error fragment, not a 500.
	form := url.Values{"prom": {"https://prom.test/"}, "start_date": {"2026-01-02"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/report", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /report bad window: status = %d, want 200 (error fragment)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Errorf("POST /report bad window: want an error fragment, got %q", rec.Body.String())
	}
}
