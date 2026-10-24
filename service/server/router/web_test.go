package router

import (
	"bytes"
	"compress/gzip"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func TestDualInterfaceAssets(t *testing.T) {
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	_, _ = z.Write([]byte("classic-js"))
	_ = z.Close()
	files := fstest.MapFS{
		"index.html":          {Data: []byte("modern-html")},
		"static/app.js":       {Data: []byte("modern-js")},
		"favicon.ico":         {Data: []byte("icon")},
		"legacy/index.html":   {Data: []byte("classic-html")},
		"legacy/js/app.js.gz": {Data: compressed.Bytes()},
	}
	r := gin.New()
	if err := serveWebFiles(r, files); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"/": "modern-html", "/static/app.js": "modern-js", "/legacy/": "classic-html", "/legacy/static/js/app.js": "classic-js", "/favicon.ico": "icon"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Body.String() != want {
			t.Errorf("%s: code=%d body=%q", path, w.Code, w.Body.String())
		}
	}
	redirect := httptest.NewRecorder()
	r.ServeHTTP(redirect, httptest.NewRequest("GET", "/legacy", nil))
	if redirect.Code != 307 || redirect.Header().Get("Location") != "legacy/" {
		t.Fatal("legacy redirect must retain reverse-proxy prefix")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("If-None-Match", w.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	r.ServeHTTP(cached, request)
	if cached.Code != 304 {
		t.Fatalf("index cache must revalidate: %d", cached.Code)
	}
}

func TestExternalLegacyInterfaceLayout(t *testing.T) {
	r := gin.New()
	if err := serveWebFiles(r, fstest.MapFS{"index.html": {Data: []byte("old")}, "js/app.js": {Data: []byte("old-js")}}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/static/js/app.js", nil))
	if w.Code != 200 || w.Body.String() != "old-js" {
		t.Fatalf("legacy --web-dir layout failed: %d %q", w.Code, w.Body.String())
	}
}
