package main

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
)

// embeddedFS holds every asset the web service serves: the HTML templates and the
// vendored static files (htmx.js, CSS). Embedding them keeps jobreport-web a single
// self-contained binary with no runtime file dependencies.
//
//go:embed templates/*.html static/*
var embeddedFS embed.FS

// templates is the parsed template set. index.html is the full page; the "urls" and
// "report" named templates are htmx swap fragments.
var templates = template.Must(template.ParseFS(embeddedFS, "templates/*.html"))

// staticFS is the embedded static/ subtree, served at /static/.
var staticFS = mustSub(embeddedFS, "static")

// mustSub returns the named sub-filesystem or panics; used for package-level init.
func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(fmt.Sprintf("sub fs %q: %v", dir, err))
	}
	return sub
}

// indexData is the data passed to the index page template.
type indexData struct {
	URLs []string
}

// renderIndex writes the full index page, with the Prometheus URL dropdown populated
// from urls.
func renderIndex(w io.Writer, urls []string) error {
	return templates.ExecuteTemplate(w, "index.html", indexData{URLs: urls})
}

// renderURLs writes the "urls" fragment: the <select id="prom"> populated from urls.
// It is the htmx swap target after a URL is added.
func renderURLs(w io.Writer, urls []string) error {
	return templates.ExecuteTemplate(w, "urls", urls)
}

// renderReport writes the "report" fragment: a <pre> with the (auto-escaped) report
// output.
func renderReport(w io.Writer, output string) error {
	return templates.ExecuteTemplate(w, "report", output)
}

// renderError writes the "error" fragment: a <pre class="error"> with the (auto-
// escaped) message. It is returned (with HTTP 200) so htmx swaps it into view
// instead of discarding a non-2xx response.
func renderError(w io.Writer, msg string) error {
	return templates.ExecuteTemplate(w, "error", msg)
}
