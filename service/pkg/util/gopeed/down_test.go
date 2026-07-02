package gopeed

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newRangeTestServer(payload []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "favicon.ico", time.Unix(0, 0), bytes.NewReader(payload))
	}))
}

func newTestRequest(url string) *Request {
	return &Request{
		Method: "get",
		URL:    url,
		Header: map[string]string{
			"User-Agent": "v2rayA-Plus test",
		},
	}
}

func testPayload() []byte {
	return bytes.Repeat([]byte("v2rayA-Plus gopeed range download test\n"), 8192)
}

func TestDown(t *testing.T) {
	payload := testPayload()
	server := newRangeTestServer(payload)
	defer server.Close()

	output := filepath.Join(t.TempDir(), "favicon.ico")
	if err := Down(newTestRequest(server.URL+"/favicon.ico"), output); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded payload mismatch: got %d bytes, want %d bytes", len(got), len(payload))
	}
}

func TestResolve(t *testing.T) {
	payload := testPayload()
	server := newRangeTestServer(payload)
	defer server.Close()

	got, err := Resolve(newTestRequest(server.URL + "/favicon.ico"))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Name != "favicon.ico" {
		t.Fatalf("Resolve().Name = %q, want %q", got.Name, "favicon.ico")
	}
	if got.Size != int64(len(payload)) {
		t.Fatalf("Resolve().Size = %d, want %d", got.Size, len(payload))
	}
	if !got.Range {
		t.Fatal("Resolve().Range = false, want true")
	}
}

func TestSparseWrite(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "test.data"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()

	if err := file.Truncate(1024 * 1024); err != nil {
		t.Fatalf("Truncate() error = %v", err)
	}
	if _, err := file.WriteAt([]byte{1, 2, 3, 4, 5}, 128*1024); err != nil {
		t.Fatalf("WriteAt() error = %v", err)
	}
}
