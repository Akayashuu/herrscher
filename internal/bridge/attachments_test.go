package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/Akayashuu/dctl"
)

// allowTestHost registers srv's host as an accepted CDN host for the duration of
// the test, so validateCDNURL passes against the local TLS server.
func allowTestHost(t *testing.T, srv *httptest.Server) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	discordCDNHosts[u.Hostname()] = true
	t.Cleanup(func() { delete(discordCDNHosts, u.Hostname()) })
}

func TestDownloadImagesFetchesOnlyImages(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()
	allowTestHost(t, srv)

	dir := t.TempDir()
	m := dctl.Message{
		ID: "42",
		Attachments: []dctl.Attachment{
			{ID: "a1", Filename: "shot.png", URL: srv.URL + "/shot.png", ContentType: "image/png", Size: 7},
			{ID: "a2", Filename: "notes.txt", URL: srv.URL + "/notes.txt", ContentType: "text/plain", Size: 3},
		},
	}
	paths, err := downloadImages(context.Background(), srv.Client(), m, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("want 1 image path, got %d: %v", len(paths), paths)
	}
	if filepath.Base(paths[0]) != "42-a1-shot.png" {
		t.Errorf("unexpected dest name: %s", paths[0])
	}
	b, err := os.ReadFile(paths[0])
	if err != nil || string(b) != "PNGDATA" {
		t.Errorf("downloaded content = %q, err=%v", b, err)
	}
}

func TestDownloadImagesOrderAndCollision(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer srv.Close()
	allowTestHost(t, srv)

	m := dctl.Message{
		ID: "7",
		Attachments: []dctl.Attachment{
			{ID: "x", Filename: "shot.png", URL: srv.URL + "/1", ContentType: "image/png", Size: 2},
			{ID: "y", Filename: "shot.png", URL: srv.URL + "/2", ContentType: "image/png", Size: 2},
		},
	}
	paths, err := downloadImages(context.Background(), srv.Client(), m, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("same-named images should not collide, got %v", paths)
	}
	if paths[0] == paths[1] {
		t.Fatalf("dest collision: %v", paths)
	}
	// Order must follow message order (load-bearing for withAttachments numbering).
	if b, _ := os.ReadFile(paths[0]); string(b) != "/1" {
		t.Errorf("first path content = %q, want /1", b)
	}
}

func TestDownloadImagesHTTPError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	allowTestHost(t, srv)

	m := dctl.Message{
		ID: "9",
		Attachments: []dctl.Attachment{
			{ID: "a", Filename: "bad.png", URL: srv.URL + "/bad", ContentType: "image/png", Size: 2},
			{ID: "b", Filename: "good.png", URL: srv.URL + "/good", ContentType: "image/png", Size: 2},
		},
	}
	paths, err := downloadImages(context.Background(), srv.Client(), m, t.TempDir())
	if err == nil {
		t.Fatal("want error from the 404 fetch")
	}
	if len(paths) != 1 { // the good one still comes through
		t.Fatalf("want 1 surviving path, got %v", paths)
	}
}

func TestDownloadImagesRejectsNonCDN(t *testing.T) {
	m := dctl.Message{
		ID: "1",
		Attachments: []dctl.Attachment{
			{ID: "a", Filename: "x.png", URL: "https://169.254.169.254/latest", ContentType: "image/png", Size: 2},
		},
	}
	paths, err := downloadImages(context.Background(), http.DefaultClient, m, t.TempDir())
	if err == nil || len(paths) != 0 {
		t.Fatalf("non-CDN url must be rejected, got paths=%v err=%v", paths, err)
	}
}

func TestValidateCDNURL(t *testing.T) {
	ok := []string{
		"https://cdn.discordapp.com/attachments/1/2/a.png",
		"https://media.discordapp.net/attachments/1/2/a.png",
	}
	for _, u := range ok {
		if err := validateCDNURL(u); err != nil {
			t.Errorf("validateCDNURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{
		"http://cdn.discordapp.com/a.png",          // not https
		"https://evil.com/a.png",                   // wrong host
		"https://cdn.discordapp.com.evil.com/a.png", // suffix trick
		"file:///etc/passwd",
		"://bad",
	}
	for _, u := range bad {
		if err := validateCDNURL(u); err == nil {
			t.Errorf("validateCDNURL(%q) = nil, want error", u)
		}
	}
}

func TestFetchOneRemovesPartialFileOnCopyError(t *testing.T) {
	// Server sends a couple of bytes then hijacks and slams the connection, so
	// io.Copy fails mid-stream after the file has been created.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "999999")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("xx"))
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	allowTestHost(t, srv)

	dir := t.TempDir()
	a := dctl.Attachment{ID: "a", Filename: "shot.png", URL: srv.URL + "/shot.png", ContentType: "image/png", Size: 2}
	if _, err := fetchOne(context.Background(), srv.Client(), a, "1", dir); err == nil {
		t.Fatal("want copy error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("partial file not cleaned up: %v", entries)
	}
}

func TestDownloadImagesContextCancelled(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data"))
	}))
	defer srv.Close()
	allowTestHost(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the fetch
	m := dctl.Message{
		ID:          "1",
		Attachments: []dctl.Attachment{{ID: "a", Filename: "x.png", URL: srv.URL + "/x.png", ContentType: "image/png", Size: 4}},
	}
	paths, err := downloadImages(ctx, srv.Client(), m, t.TempDir())
	if err == nil || len(paths) != 0 {
		t.Fatalf("cancelled ctx should abort the fetch, got paths=%v err=%v", paths, err)
	}
}

func TestFetchOneTruncatesAtLimit(t *testing.T) {
	// Server lies: body is larger than maxAttachmentBytes; LimitReader must cap it.
	big := maxAttachmentBytes + 100
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64<<10)
		for i := range buf {
			buf[i] = 'A'
		}
		for written := 0; written < big; written += len(buf) {
			_, _ = w.Write(buf)
		}
	}))
	defer srv.Close()
	allowTestHost(t, srv)

	dir := t.TempDir()
	// Size under the cap so the pre-check passes; the body itself is oversized.
	a := dctl.Attachment{ID: "a", Filename: "x.png", URL: srv.URL + "/x.png", ContentType: "image/png", Size: 10}
	p, err := fetchOne(context.Background(), srv.Client(), a, "1", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != int64(maxAttachmentBytes) {
		t.Errorf("file size = %d, want capped at %d", fi.Size(), maxAttachmentBytes)
	}
}

func TestDownloadImagesSkipsOversized(t *testing.T) {
	dir := t.TempDir()
	m := dctl.Message{
		ID: "1",
		Attachments: []dctl.Attachment{
			{ID: "a", Filename: "big.png", URL: "https://cdn.discordapp.com/big.png", ContentType: "image/png", Size: maxAttachmentBytes + 1},
		},
	}
	paths, err := downloadImages(context.Background(), http.DefaultClient, m, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("oversized image should be skipped, got %v", paths)
	}
}

func TestDownloadImagesNoImagesNoDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	m := dctl.Message{ID: "1", Attachments: []dctl.Attachment{{Filename: "a.txt", ContentType: "text/plain"}}}
	paths, err := downloadImages(context.Background(), http.DefaultClient, m, dir)
	if err != nil || paths != nil {
		t.Fatalf("want nil/nil, got %v / %v", paths, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should not be created when there are no images")
	}
}

func TestSanitizePreventsEscape(t *testing.T) {
	for _, in := range []string{"../../etc/passwd", "/abs/path", "..", "a/b/c.png"} {
		got := sanitize(in)
		if filepath.Base(got) != got || got == ".." || got == "" {
			t.Errorf("sanitize(%q) = %q is unsafe", in, got)
		}
	}
	if got := sanitize("..."); got != "file" {
		t.Errorf(`sanitize("...") = %q, want "file"`, got)
	}
	if got := sanitize(".hidden"); got != "hidden" {
		t.Errorf(`sanitize(".hidden") = %q, want "hidden"`, got)
	}
}
