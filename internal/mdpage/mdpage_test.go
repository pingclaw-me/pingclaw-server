package mdpage

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "mdpage-*")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

// --- FragmentHandler tests ---

func TestFragmentHandler(t *testing.T) {
	md := tempFile(t, "# Hello\n\nSome **bold** text.\n")

	h := NewFragmentHandler(md)
	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<h1>Hello</h1>") {
		t.Fatalf("expected <h1>Hello</h1>, got %s", body)
	}
	if !strings.Contains(body, "<strong>bold</strong>") {
		t.Fatalf("expected <strong>bold</strong>, got %s", body)
	}
}

func TestFragmentHandlerMissingFile(t *testing.T) {
	h := NewFragmentHandler("/nonexistent/path/file.md")
	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 500 {
		t.Fatalf("expected 500 for missing file, got %d", w.Code)
	}
}

func TestFragmentHandlerEmptyMarkdown(t *testing.T) {
	md := tempFile(t, "")

	h := NewFragmentHandler(md)
	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 200 {
		t.Fatalf("expected 200 for empty markdown, got %d", w.Code)
	}
}

// --- Handler (full page) tests ---

func TestHandler(t *testing.T) {
	tmpl := tempFile(t, `<html><body><p>Updated: {{.LastUpdated}}</p><div>{{.Content}}</div></body></html>`)
	md := tempFile(t, "## Privacy\n\nWe respect your data.\n")

	h := NewHandler(tmpl, md)
	r := httptest.NewRequest("GET", "/privacy", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "<h2>Privacy</h2>") {
		t.Fatalf("expected rendered markdown, got %s", body)
	}
	if !strings.Contains(body, "We respect your data.") {
		t.Fatalf("expected content text, got %s", body)
	}
	// LastUpdated should be populated from file mtime
	if strings.Contains(body, "{{.LastUpdated}}") {
		t.Fatal("template placeholder should be replaced")
	}
	// Should contain a date-like string (month name)
	if !strings.Contains(body, "Updated: ") {
		t.Fatal("expected Updated: prefix")
	}
}

func TestHandlerMissingTemplate(t *testing.T) {
	md := tempFile(t, "# Test\n")

	h := NewHandler("/nonexistent/template.html", md)
	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 500 {
		t.Fatalf("expected 500 for missing template, got %d", w.Code)
	}
}

func TestHandlerMissingContent(t *testing.T) {
	tmpl := tempFile(t, `<html>{{.Content}}</html>`)

	h := NewHandler(tmpl, "/nonexistent/content.md")
	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 500 {
		t.Fatalf("expected 500 for missing content, got %d", w.Code)
	}
}

func TestHandlerBadTemplate(t *testing.T) {
	tmpl := tempFile(t, `<html>{{.Broken`)
	md := tempFile(t, "# Test\n")

	h := NewHandler(tmpl, md)
	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 500 {
		t.Fatalf("expected 500 for bad template, got %d", w.Code)
	}
}

func TestHandlerContentType(t *testing.T) {
	tmpl := tempFile(t, `{{.Content}}`)
	md := tempFile(t, "Hello\n")

	h := NewHandler(tmpl, md)
	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html, got %s", ct)
	}
}

func TestHandlerReadsOnEveryRequest(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "content.md")
	tmplPath := filepath.Join(dir, "template.html")

	os.WriteFile(tmplPath, []byte(`<div>{{.Content}}</div>`), 0644)
	os.WriteFile(mdPath, []byte("First version\n"), 0644)

	h := NewHandler(tmplPath, mdPath)

	// First request
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w1.Body.String(), "First version") {
		t.Fatal("expected first version")
	}

	// Update the file
	os.WriteFile(mdPath, []byte("Second version\n"), 0644)

	// Second request should see updated content
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w2.Body.String(), "Second version") {
		t.Fatal("expected second version after file update")
	}
}

// --- Markdown handler tests ---

func TestMarkdownHandler(t *testing.T) {
	md := tempFile(t, "# Privacy\n\nWe respect your data.\n")

	h := NewMarkdownHandler(md)
	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Fatalf("expected text/markdown, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "# Privacy") {
		t.Fatalf("expected raw markdown, got %s", body)
	}
	if strings.Contains(body, "<h1>") {
		t.Fatal("should return raw markdown, not HTML")
	}
}

func TestMarkdownHandlerCacheHeader(t *testing.T) {
	md := tempFile(t, "test\n")

	h := NewMarkdownHandler(md)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

	cc := w.Header().Get("Cache-Control")
	if cc == "" {
		t.Fatal("expected Cache-Control header")
	}
}

func TestMarkdownHandlerMissingFile(t *testing.T) {
	h := NewMarkdownHandler("/nonexistent/file.md")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

	if w.Code != 500 {
		t.Fatalf("expected 500 for missing file, got %d", w.Code)
	}
}

// --- Verify all types implement http.Handler ---

func TestHandlerImplementsHTTPHandler(t *testing.T) {
	tmpl := tempFile(t, `{{.Content}}`)
	md := tempFile(t, "test\n")

	var _ http.Handler = NewHandler(tmpl, md)
	var _ http.Handler = NewFragmentHandler(md)
	var _ http.Handler = NewMarkdownHandler(md)
}
