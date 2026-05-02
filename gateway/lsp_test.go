package gateway

import (
	"encoding/json"
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

func TestScanGoDocumentSymbols(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pkg", "outline.go"), `package pkg

type Runner struct{}
func (Runner) Run() {}
func Build() {}
`)
	symbols, err := scanGoDocumentSymbols(root, "pkg/outline.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 3 || symbols[0].Name != "Runner" || symbols[1].Kind != "method" || symbols[1].Container != "Runner" {
		t.Fatalf("document symbols가 이상해요: %+v", symbols)
	}
	if _, err := scanGoDocumentSymbols(root, "../outside.go"); err == nil {
		t.Fatal("project_root 밖 path는 거부해야 해요")
	}
}

func TestHandleLSPDocumentSymbols(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), `package main
func Main() {}
`)
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/lsp/document-symbols?project_root="+root+"&path=main.go", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got LSPSymbolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Symbols) != 1 || got.Symbols[0].Name != "Main" {
		t.Fatalf("document symbol 응답이 이상해요: %+v", got)
	}
}

func TestScanGoNavigationDiagnosticsAndHover(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pkg", "nav.go"), `package pkg

// Runner는 작업을 실행해요.
type Runner struct{}

// Run은 작업을 시작해요.
func (Runner) Run() {}

func Use(r Runner) {
	r.Run()
}
`)
	writeFile(t, filepath.Join(root, "bad.go"), `package bad
func Broken( {
`)

	defs, err := scanGoDefinitions(root, "Runner.Run", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Kind != "method" || defs[0].Container != "Runner" {
		t.Fatalf("definition scan이 이상해요: %+v", defs)
	}

	refs, err := scanGoReferences(root, "Run", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) < 2 {
		t.Fatalf("reference scan이 부족해요: %+v", refs)
	}

	diagnostics, err := scanGoDiagnostics(root, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) == 0 || diagnostics[0].Source != "go/parser" {
		t.Fatalf("diagnostics scan이 이상해요: %+v", diagnostics)
	}

	hover, err := scanGoHover(root, "Runner.Run")
	if err != nil {
		t.Fatal(err)
	}
	if !hover.Found || hover.Kind != "method" || hover.Documentation == "" || hover.Signature == "" {
		t.Fatalf("hover scan이 이상해요: %+v", hover)
	}
}
