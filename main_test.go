package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestApp(t *testing.T, maxUpload int64) *app {
	t.Helper()
	storage := t.TempDir()
	root, err := os.OpenRoot(storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &app{
		root: root, storageRoot: storage, listenHost: "0.0.0.0", port: "3000",
		maxUploadBytes: maxUpload,
	}
}

func request(t *testing.T, handler http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestAPIIsAvailableWithoutAuthentication(t *testing.T) {
	application := newTestApp(t, 64)
	response := request(t, application.routes(), http.MethodGet, "/api/config", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	response = request(t, application.routes(), http.MethodGet, "/api/list", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", response.Code, http.StatusOK)
	}
	response = request(t, application.routes(), http.MethodGet, "/api/download-folder", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("root zip status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestUploadListDownloadZipAndDelete(t *testing.T) {
	application := newTestApp(t, 64)
	server := application.routes()

	response := request(t, server, http.MethodPost, "/api/upload-file?path=docs&relative=folder/hello.txt", strings.NewReader("hello world"))
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
	}

	response = request(t, server, http.MethodGet, "/api/list?path=docs/folder", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", response.Code, response.Body.String())
	}
	var listing struct {
		Items []directoryItem `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Items) != 1 || listing.Items[0].Name != "hello.txt" || listing.Items[0].Type != "file" {
		t.Fatalf("unexpected list result: %+v", listing.Items)
	}

	response = request(t, server, http.MethodGet, "/api/download-file?path=docs/folder/hello.txt", nil)
	if response.Code != http.StatusOK || response.Body.String() != "hello world" {
		t.Fatalf("download status=%d body=%q", response.Code, response.Body.String())
	}

	response = request(t, server, http.MethodGet, "/api/download-folder?path=docs", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("zip status = %d", response.Code)
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 2 {
		t.Fatalf("zip contains %d entries, want directory and file", len(archive.File))
	}

	response = request(t, server, http.MethodDelete, "/api/item?path=docs", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", response.Code, response.Body.String())
	}
	if _, err := application.root.Stat("docs"); !os.IsNotExist(err) {
		t.Fatalf("deleted path still exists or unexpected error: %v", err)
	}
}

func TestPathTraversalAndRootDeletionAreRejected(t *testing.T) {
	application := newTestApp(t, 64)
	server := application.routes()
	tests := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/list?path=..%2Foutside"},
		{http.MethodGet, "/api/download-file?path=%2Fetc%2Fpasswd"},
		{http.MethodDelete, "/api/item?path="},
	}
	for _, test := range tests {
		response := request(t, server, test.method, test.target, nil)
		if response.Code < 400 {
			t.Fatalf("%s returned status %d, want a client error", test.target, response.Code)
		}
	}
}

func TestSymlinkCannotEscapeStorageRoot(t *testing.T) {
	application := newTestApp(t, 64)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := application.root.Symlink(outside, "escape"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	server := application.routes()
	for _, target := range []string{
		"/api/download-file?path=escape",
		"/api/upload-file?path=escape&relative=created.txt",
		"/api/item?path=escape",
	} {
		method := http.MethodGet
		body := io.Reader(nil)
		if strings.Contains(target, "upload-file") {
			method = http.MethodPost
			body = strings.NewReader("no")
		} else if strings.Contains(target, "/api/item") {
			method = http.MethodDelete
		}
		response := request(t, server, method, target, body)
		if response.Code < 400 {
			t.Fatalf("%s returned status %d, want a client error", target, response.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(outside), "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("write escaped storage root: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("external symlink target was removed: %v", err)
	}
}

func TestUploadLimitDoesNotCommitPartialFile(t *testing.T) {
	application := newTestApp(t, 4)
	response := request(t, application.routes(), http.MethodPost, "/api/upload-file?relative=large.bin", strings.NewReader("12345"))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusRequestEntityTooLarge, response.Body.String())
	}
	if _, err := application.root.Stat("large.bin"); !os.IsNotExist(err) {
		t.Fatalf("partial upload was committed: %v", err)
	}
}

func TestRootPageIsServed(t *testing.T) {
	application := newTestApp(t, 64)
	response := request(t, application.routes(), http.MethodGet, "/", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "LAN 文件服务器") {
		t.Fatalf("root page status=%d body length=%d", response.Code, response.Body.Len())
	}
}
