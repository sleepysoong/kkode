package gateway

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// LSPSymbolDTO는 외부 패널이 코드 탐색 UI를 만들 때 쓰는 LSP-style symbol 항목이에요.
type LSPSymbolDTO struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Container string `json:"container,omitempty"`
}

type LSPSymbolListResponse struct {
	Symbols []LSPSymbolDTO `json:"symbols"`
}

type LSPLocationListResponse struct {
	Locations []LSPSymbolDTO `json:"locations"`
}

type LSPReferenceDTO struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Excerpt string `json:"excerpt,omitempty"`
}

type LSPReferenceListResponse struct {
	References []LSPReferenceDTO `json:"references"`
}

func (s *Server) handleLSP(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 2 {
		writeError(w, r, http.StatusNotFound, "not_found", "lsp endpoint를 찾을 수 없어요")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 lsp method예요")
		return
	}
	root := strings.TrimSpace(r.URL.Query().Get("project_root"))
	if root == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_lsp_request", "project_root가 필요해요")
		return
	}
	switch parts[1] {
	case "symbols":
		limit := queryLimit(r, "limit", 200, 1000)
		symbols, err := scanGoSymbols(root, strings.TrimSpace(r.URL.Query().Get("query")), limit)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "scan_symbols_failed", err.Error())
			return
		}
		writeJSON(w, LSPSymbolListResponse{Symbols: symbols})
	case "document-symbols":
		symbols, err := scanGoDocumentSymbols(root, strings.TrimSpace(r.URL.Query().Get("path")))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_document_symbols_failed", err.Error())
			return
		}
		writeJSON(w, LSPSymbolListResponse{Symbols: symbols})
	case "definitions":
		symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
		definitions, err := scanGoDefinitions(root, symbol, queryLimit(r, "limit", 50, 200))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_definitions_failed", err.Error())
			return
		}
		writeJSON(w, LSPLocationListResponse{Locations: definitions})
	case "references":
		symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
		references, err := scanGoReferences(root, symbol, queryLimit(r, "limit", 100, 1000))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_references_failed", err.Error())
			return
		}
		writeJSON(w, LSPReferenceListResponse{References: references})
	default:
		writeError(w, r, http.StatusNotFound, "not_found", "lsp endpoint를 찾을 수 없어요")
	}
}

func scanGoSymbols(root string, query string, limit int) ([]LSPSymbolDTO, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(query)
	if limit <= 0 {
		limit = 200
	}
	fset := token.NewFileSet()
	out := []LSPSymbolDTO{}
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(out) >= limit {
			return fs.SkipAll
		}
		if entry.IsDir() {
			if shouldSkipLSPDir(entry.Name()) && path != absRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(absRoot, path)
		appendSymbol := func(name string, kind string, pos token.Pos, container string) {
			if len(out) >= limit || name == "" {
				return
			}
			if needle != "" && !strings.Contains(strings.ToLower(name), needle) && !strings.Contains(strings.ToLower(container), needle) {
				return
			}
			p := fset.Position(pos)
			out = append(out, LSPSymbolDTO{Name: name, Kind: kind, File: filepath.ToSlash(rel), Line: p.Line, Column: p.Column, Container: container})
		}
		collectGoSymbols(file, appendSymbol)
		return nil
	})
	return out, err
}

func collectGoSymbols(file *ast.File, appendSymbol func(name string, kind string, pos token.Pos, container string)) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind := "function"
			container := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				container = receiverName(d.Recv.List[0].Type)
			}
			appendSymbol(d.Name.Name, kind, d.Name.Pos(), container)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					appendSymbol(s.Name.Name, "type", s.Name.Pos(), "")
				case *ast.ValueSpec:
					kind := strings.ToLower(d.Tok.String())
					for _, name := range s.Names {
						appendSymbol(name.Name, kind, name.Pos(), "")
					}
				}
			}
		}
	}
}

func scanGoDocumentSymbols(root string, relPath string) ([]LSPSymbolDTO, error) {
	if strings.TrimSpace(relPath) == "" {
		return nil, fmt.Errorf("path가 필요해요")
	}
	if filepath.IsAbs(relPath) {
		return nil, fmt.Errorf("path는 project_root 기준 상대 경로여야 해요: %s", relPath)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	path := filepath.Clean(filepath.Join(absRoot, relPath))
	if path != absRoot && !strings.HasPrefix(path, absRoot+string(filepath.Separator)) {
		return nil, fmt.Errorf("path가 project_root 밖으로 벗어나요: %s", relPath)
	}
	if !strings.HasSuffix(path, ".go") {
		return nil, fmt.Errorf("go 파일만 document symbol을 지원해요: %s", relPath)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(absRoot, path)
	out := []LSPSymbolDTO{}
	appendSymbol := func(name string, kind string, pos token.Pos, container string) {
		if name == "" {
			return
		}
		p := fset.Position(pos)
		out = append(out, LSPSymbolDTO{Name: name, Kind: kind, File: filepath.ToSlash(rel), Line: p.Line, Column: p.Column, Container: container})
	}
	collectGoSymbols(file, appendSymbol)
	return out, nil
}

func scanGoDefinitions(root string, symbol string, limit int) ([]LSPSymbolDTO, error) {
	if strings.TrimSpace(symbol) == "" {
		return nil, fmt.Errorf("symbol이 필요해요")
	}
	if limit <= 0 {
		limit = 50
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	out := []LSPSymbolDTO{}
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(out) >= limit {
			return fs.SkipAll
		}
		if entry.IsDir() {
			if shouldSkipLSPDir(entry.Name()) && path != absRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(absRoot, path)
		appendSymbol := func(name string, kind string, pos token.Pos, container string) {
			if len(out) >= limit || !matchesLSPSymbol(symbol, name, container) {
				return
			}
			p := fset.Position(pos)
			out = append(out, LSPSymbolDTO{Name: name, Kind: kind, File: filepath.ToSlash(rel), Line: p.Line, Column: p.Column, Container: container})
		}
		collectGoSymbols(file, appendSymbol)
		return nil
	})
	return out, err
}

func scanGoReferences(root string, symbol string, limit int) ([]LSPReferenceDTO, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, fmt.Errorf("symbol이 필요해요")
	}
	if limit <= 0 {
		limit = 100
	}
	target := symbol
	if dot := strings.LastIndex(symbol, "."); dot >= 0 && dot+1 < len(symbol) {
		target = symbol[dot+1:]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	out := []LSPReferenceDTO{}
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(out) >= limit {
			return fs.SkipAll
		}
		if entry.IsDir() {
			if shouldSkipLSPDir(entry.Name()) && path != absRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		source, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(absRoot, path)
		lines := readFileLines(path)
		seen := map[token.Pos]bool{}
		addReference := func(name string, kind string, pos token.Pos) {
			if len(out) >= limit || name != target || seen[pos] {
				return
			}
			seen[pos] = true
			p := fset.Position(pos)
			out = append(out, LSPReferenceDTO{Name: name, Kind: kind, File: filepath.ToSlash(rel), Line: p.Line, Column: p.Column, Excerpt: lineExcerpt(lines, p.Line)})
		}
		ast.Inspect(source, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SelectorExpr:
				addReference(n.Sel.Name, "selector", n.Sel.Pos())
				return true
			case *ast.Ident:
				addReference(n.Name, "identifier", n.Pos())
			}
			return true
		})
		return nil
	})
	return out, err
}

func matchesLSPSymbol(symbol string, name string, container string) bool {
	if symbol == name {
		return true
	}
	return container != "" && symbol == container+"."+name
}

func readFileLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

func lineExcerpt(lines []string, line int) string {
	if line <= 0 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

func shouldSkipLSPDir(name string) bool {
	switch name {
	case ".git", ".kkode", ".omx", ".serena", "node_modules", "vendor", "tmp", "dist", "build", "coverage", "target":
		return true
	default:
		return false
	}
}

func receiverName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return receiverName(v.X)
	case *ast.IndexExpr:
		return receiverName(v.X)
	case *ast.IndexListExpr:
		return receiverName(v.X)
	case *ast.SelectorExpr:
		return v.Sel.Name
	default:
		return ""
	}
}
