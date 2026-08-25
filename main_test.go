package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGeneratesSitemapAndDiscoveryReport(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		_, _ = w.Write([]byte(`<title>Home</title><a href="/about?campaign=x#team">About</a><a href="/asset.pdf">PDF</a>`))
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<title>About us</title><a href="/">Home</a><a href="https://example.org/away">Away</a>`))
	})
	mux.HandleFunc("/asset.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("not a page"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	dir := t.TempDir()
	sitemap := filepath.Join(dir, "public", "sitemap.xml")
	discovery := filepath.Join(dir, "artifacts", "pages.json")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-o", sitemap, "-discover-output", discovery, server.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}

	xmlData, err := os.ReadFile(sitemap)
	if err != nil {
		t.Fatal(err)
	}
	var got xmlURLSet
	if err := xml.Unmarshal(xmlData, &got); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, xmlData)
	}
	if len(got.URLs) != 2 {
		t.Fatalf("got %d sitemap URLs, want 2: %s", len(got.URLs), xmlData)
	}
	if strings.Contains(string(xmlData), "campaign") || strings.Contains(string(xmlData), "asset.pdf") {
		t.Fatalf("sitemap contains query or non-HTML URL: %s", xmlData)
	}
	if got.URLs[0].LastMod != "2006-01-02" {
		t.Fatalf("lastmod=%q", got.URLs[0].LastMod)
	}

	jsonData, err := os.ReadFile(discovery)
	if err != nil {
		t.Fatal(err)
	}
	var report discoveryReport
	if err := json.Unmarshal(jsonData, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Pages) != 2 || len(report.Errors) != 1 {
		t.Fatalf("pages=%d errors=%d", len(report.Pages), len(report.Errors))
	}
	if report.Pages[1].Title != "About us" {
		t.Fatalf("title=%q", report.Pages[1].Title)
	}
}

func TestRunFailOnBroken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/bad" {
			http.Error(w, "gone", http.StatusGone)
			return
		}
		_, _ = w.Write([]byte(`<a href="/bad">bad</a>`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-o", filepath.Join(t.TempDir(), "sitemap.xml"), "-fail-on-broken", server.URL}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
	}
}

func TestRunStdoutContainsOnlyXML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-o", "-", "-quiet", server.URL}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.HasPrefix(stdout.String(), "<?xml") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}
