// Package mdpage renders markdown content into HTML, either as a full
// page (markdown content + HTML template shell) or as a bare fragment
// for client-side injection.
//
//   NewHandler         — full page; the shell is a Go html/template with
//                        {{.Content}} and {{.LastUpdated}} placeholders.
//                        Used for pages like /privacypolicy and
//                        /termsofservice.
//
//   NewFragmentHandler — markdown rendered to HTML with no surrounding
//                        page. Used for content snippets fetched by
//                        the dashboard, e.g. /setup/ios.html.
//
// Files are read on every request so editing the markdown / template and
// reloading the page is enough — no server restart required.
package mdpage

import (
	"bytes"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"os"

	"github.com/yuin/goldmark"
)

// Handler renders the privacy policy page from markdown + template.
type Handler struct {
	templatePath string
	contentPath  string
}

func NewHandler(templatePath, contentPath string) *Handler {
	return &Handler{templatePath: templatePath, contentPath: contentPath}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tmplBytes, err := os.ReadFile(h.templatePath)
	if err != nil {
		slog.Error("mdpage: read template failed", "path", h.templatePath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	contentMD, err := os.ReadFile(h.contentPath)
	if err != nil {
		slog.Error("mdpage: read content failed", "path", h.contentPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var contentHTML bytes.Buffer
	if err := goldmark.Convert(contentMD, &contentHTML); err != nil {
		slog.Error("mdpage: markdown convert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	tmpl, err := template.New("mdpage").Parse(string(tmplBytes))
	if err != nil {
		slog.Error("mdpage: template parse failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Last-updated date is the content file's mtime — keeps it accurate
	// without requiring an extra step when the prose is edited.
	info, err := os.Stat(h.contentPath)
	lastUpdated := "—"
	if err == nil {
		lastUpdated = info.ModTime().Format("January 2, 2006")
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, struct {
		Content     template.HTML
		LastUpdated string
	}{
		Content:     template.HTML(contentHTML.String()),
		LastUpdated: lastUpdated,
	}); err != nil {
		slog.Error("mdpage: template execute failed", "error", err)
	}
}

// NewMarkdownHandler returns an http.Handler that serves the markdown
// content as JSON with a last_updated date. Used by mobile apps to
// fetch prose content and render it natively.
//
// Response: {"content": "## ...", "last_updated": "April 21, 2026"}
func NewMarkdownHandler(contentPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentMD, err := os.ReadFile(contentPath)
		if err != nil {
			slog.Error("markdown: read content failed", "path", contentPath, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		lastUpdated := "—"
		if info, err := os.Stat(contentPath); err == nil {
			lastUpdated = info.ModTime().Format("January 2, 2006")
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		json.NewEncoder(w).Encode(map[string]string{
			"content":      string(contentMD),
			"last_updated": lastUpdated,
		})
	})
}

// NewFragmentHandler returns an http.Handler that reads a markdown file
// on every request, converts it to HTML, and returns the HTML fragment
// (no page shell). Use this for content that's injected into another
// page on the client side, e.g. via fetch().
func NewFragmentHandler(contentPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentMD, err := os.ReadFile(contentPath)
		if err != nil {
			slog.Error("fragment: read content failed", "path", contentPath, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		var html bytes.Buffer
		if err := goldmark.Convert(contentMD, &html); err != nil {
			slog.Error("fragment: markdown convert failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html.Bytes())
	})
}
