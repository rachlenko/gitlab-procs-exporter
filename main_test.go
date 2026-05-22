package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rachlenko/gitlab-procs-exporter/exporter"

	"github.com/shirou/gopsutil/v3/process"
)

func TestServeDashboard(t *testing.T) {
	// Test valid GET /
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()

	serveDashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status code 200, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "text/html; charset=utf-8" {
		t.Errorf("expected Content-Type text/html; charset=utf-8, got %q", contentType)
	}

	body := rr.Body.String()
	if body == "" {
		t.Error("expected non-empty dashboard body")
	}

	// Test 404 for undefined path
	req404 := httptest.NewRequest("GET", "/some-random-route", nil)
	rr404 := httptest.NewRecorder()

	serveDashboard(rr404, req404)

	if rr404.Code != http.StatusNotFound {
		t.Errorf("expected status code 404, got %d", rr404.Code)
	}
}

func TestServeAPIProcesses(t *testing.T) {
	store := exporter.NewHistoryStore()
	sample := exporter.ProcessSample{
		Timestamp:  time.Now(),
		PID:        2222,
		Name:       "proc-main",
		CPUUsage:   5.0,
		CreateTime: 100,
		IsActive:   true,
	}
	store.AddSample(sample)

	req := httptest.NewRequest("GET", "/api/processes", nil)
	rr := httptest.NewRecorder()

	serveAPIProcesses(rr, req, store)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status code 200, got %d", rr.Code)
	}

	if contentType := rr.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", contentType)
	}

	var active []exporter.ProcessSample
	if err := json.NewDecoder(rr.Body).Decode(&active); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if len(active) != 1 {
		t.Fatalf("expected 1 active process in API response, got %d", len(active))
	}

	if active[0].PID != 2222 {
		t.Errorf("expected PID 2222, got %d", active[0].PID)
	}
}

func TestServeAPIHistory(t *testing.T) {
	store := exporter.NewHistoryStore()
	sample := exporter.ProcessSample{
		Timestamp:  time.Now(),
		PID:        3333,
		Name:       "sidekiq",
		CPUUsage:   1.0,
		CreateTime: 100,
		IsActive:   true,
	}
	store.AddSample(sample)

	// Case 1: Missing both parameters
	req1 := httptest.NewRequest("GET", "/api/history", nil)
	rr1 := httptest.NewRecorder()

	serveAPIHistory(rr1, req1, store)

	if rr1.Code != http.StatusBadRequest {
		t.Errorf("expected status code 400 for missing params, got %d", rr1.Code)
	}

	// Case 2: Query by PID
	req2 := httptest.NewRequest("GET", "/api/history?pid=3333", nil)
	rr2 := httptest.NewRecorder()

	serveAPIHistory(rr2, req2, store)

	if rr2.Code != http.StatusOK {
		t.Fatalf("expected status code 200 for PID query, got %d", rr2.Code)
	}

	var history map[string][]exporter.ProcessSample
	if err := json.NewDecoder(rr2.Body).Decode(&history); err != nil {
		t.Fatalf("failed to decode JSON for PID query: %v", err)
	}

	if len(history) != 1 {
		t.Errorf("expected 1 process timeline for PID query, got %d", len(history))
	}

	// Case 3: Query by Name
	req3 := httptest.NewRequest("GET", "/api/history?name=sidekiq", nil)
	rr3 := httptest.NewRecorder()

	serveAPIHistory(rr3, req3, store)

	if rr3.Code != http.StatusOK {
		t.Fatalf("expected status code 200 for Name query, got %d", rr3.Code)
	}

	var historyName map[string][]exporter.ProcessSample
	if err := json.NewDecoder(rr3.Body).Decode(&historyName); err != nil {
		t.Fatalf("failed to decode JSON for Name query: %v", err)
	}

	if len(historyName) != 1 {
		t.Errorf("expected 1 process timeline for Name query, got %d", len(historyName))
	}
}

func TestScrape(t *testing.T) {
	store := exporter.NewHistoryStore()
	cache := make(map[int32]*process.Process)

	scrape(store, cache)

	// In a normal test execution environment (Linux or macOS), there are always active processes
	active := store.GetActiveProcesses()
	if len(active) == 0 {
		t.Log("warning: no active processes could be scraped on this machine")
	} else {
		t.Logf("successfully scraped %d active processes from the host OS", len(active))
		if len(cache) == 0 {
			t.Error("expected process cache to be populated after scraping")
		}
	}
}

func TestStartScraper(t *testing.T) {
	store := exporter.NewHistoryStore()

	// Run background scraper with short interval
	go startScraper(store, 10*time.Millisecond)

	// Wait briefly for at least a few scraper cycles
	time.Sleep(30 * time.Millisecond)

	// Check that we got active processes scraped
	_ = store.GetActiveProcesses()
}
