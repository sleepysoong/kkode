package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestScanGoSymbolsSkipsHeavyDirsAndHonorsLimit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), `package main

type Alpha struct{}
func RunAlpha() {}
`)
	writeFile(t, filepath.Join(root, "node_modules", "skip.go"), `package skip
func ShouldNotAppear() {}
`)
	writeFile(t, filepath.Join(root, "pkg", "more.go"), `package pkg
func Beta() {}
`)
	symbols, err := scanGoSymbols(root, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 1 {
		t.Fatalf("limit=1이면 symbol 하나만 반환해야 해요: %+v", symbols)
	}
	for _, symbol := range symbols {
		if symbol.Name == "ShouldNotAppear" {
			t.Fatalf("node_modules는 scan에서 제외해야 해요: %+v", symbols)
		}
	}
}

func TestHandleLSPRejectsUnsupportedMethodAs405(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/lsp/symbols?project_root=.", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /lsp/symbols는 405여야 해요: %d %s", rec.Code, rec.Body.String())
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
