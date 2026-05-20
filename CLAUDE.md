# GitLab Process History Exporter - Developer Guidelines

Prometheus process exporter with an active 10-minute sliding cache in-memory buffer and a premium dark-mode SPA dashboard.

## Build and Quality Verification
* Build binary: `make build`
* Format code: `make fmt`
* Run linters: `make lint`
* Run unit tests: `make test`

## Project Architecture
* `main.go` — Entrypoint, configuration setup, background scrapers, and embedded Web/REST servers.
* `exporter/history.go` — Sliding 10-minute `HistoryStore` buffer designed with thread-safe lock pools.
* `exporter/collector.go` — Custom Prometheus exporter metrics reporting system with credential filtering rules.
* `dashboard/` — Front-end glassmorphic SPA utilizing Chart.js rendering timeline charts.

## Code Style & Implementation Standards
* Keep one `*_test.go` file associated directly per implementation file.
* Use `sync.RWMutex` to isolate write threads in `HistoryStore` from foreground scrapes.
* Prevent metric leaks by filtering out dynamic sensitive tokens in `IsSecretKey()`.
* Keep process handlers lightweight: persist `*process.Process` pointers inside scraping caches to avoid reading Jiffies baseline tables repetitively.
* Any temporary testing files MUST be cleaned up via deferred closures.
