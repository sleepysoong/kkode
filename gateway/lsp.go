package gateway

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
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

type LSPDiagnosticDTO struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type LSPDiagnosticListResponse struct {
	Diagnostics []LSPDiagnosticDTO `json:"diagnostics"`
}

type LSPHoverResponse struct {
	Found         bool   `json:"found"`
	Symbol        string `json:"symbol,omitempty"`
	Kind          string `json:"kind,omitempty"`
	File          string `json:"file,omitempty"`
	Line          int    `json:"line,omitempty"`
	Column        int    `json:"column,omitempty"`
	Container     string `json:"container,omitempty"`
	Signature     string `json:"signature,omitempty"`
	Documentation string `json:"documentation,omitempty"`
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
	case "diagnostics":
		diagnostics, err := scanGoDiagnostics(root, strings.TrimSpace(r.URL.Query().Get("path")), queryLimit(r, "limit", 200, 1000))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_diagnostics_failed", err.Error())
			return
		}
		writeJSON(w, LSPDiagnosticListResponse{Diagnostics: diagnostics})
	case "hover":
		hover, err := scanGoHover(root, strings.TrimSpace(r.URL.Query().Get("symbol")))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_hover_failed", err.Error())
			return
		}
		writeJSON(w, hover)
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
	err = walkParsedGoFiles(absRoot, fset, 0, func() bool { return len(out) >= limit }, func(parsed parsedGoFile) error {
		if parsed.Err != nil {
			return nil
		}
		appendSymbol := func(name string, kind string, pos token.Pos, container string) {
			if len(out) >= limit || name == "" {
				return
			}
			if needle != "" && !strings.Contains(strings.ToLower(name), needle) && !strings.Contains(strings.ToLower(container), needle) {
				return
			}
			p := fset.Position(pos)
			out = append(out, LSPSymbolDTO{Name: name, Kind: kind, File: parsed.Rel, Line: p.Line, Column: p.Column, Container: container})
		}
		collectGoSymbols(parsed.File, appendSymbol)
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
	err = walkParsedGoFiles(absRoot, fset, 0, func() bool { return len(out) >= limit }, func(parsed parsedGoFile) error {
		if parsed.Err != nil {
			return nil
		}
		appendSymbol := func(name string, kind string, pos token.Pos, container string) {
			if len(out) >= limit || !matchesLSPSymbol(symbol, name, container) {
				return
			}
			p := fset.Position(pos)
			out = append(out, LSPSymbolDTO{Name: name, Kind: kind, File: parsed.Rel, Line: p.Line, Column: p.Column, Container: container})
		}
		collectGoSymbols(parsed.File, appendSymbol)
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
	err = walkParsedGoFiles(absRoot, fset, 0, func() bool { return len(out) >= limit }, func(parsed parsedGoFile) error {
		if parsed.Err != nil {
			return nil
		}
		lines := readFileLines(parsed.Path)
		seen := map[token.Pos]bool{}
		addReference := func(name string, kind string, pos token.Pos) {
			if len(out) >= limit || name != target || seen[pos] {
				return
			}
			seen[pos] = true
			p := fset.Position(pos)
			out = append(out, LSPReferenceDTO{Name: name, Kind: kind, File: parsed.Rel, Line: p.Line, Column: p.Column, Excerpt: lineExcerpt(lines, p.Line)})
		}
		ast.Inspect(parsed.File, func(node ast.Node) bool {
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

func scanGoDiagnostics(root string, relPath string, limit int) ([]LSPDiagnosticDTO, error) {
	if limit <= 0 {
		limit = 200
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	out := []LSPDiagnosticDTO{}
	visit := func(path string) error {
		if len(out) >= limit {
			return fs.SkipAll
		}
		rel, _ := filepath.Rel(absRoot, path)
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		_, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err == nil {
			return nil
		}
		appendParseDiagnostics(&out, rel, err, limit)
		return nil
	}
	if strings.TrimSpace(relPath) != "" {
		path, err := resolveRelativeGoFile(absRoot, relPath)
		if err != nil {
			return nil, err
		}
		return out, visit(path)
	}
	fset := token.NewFileSet()
	err = walkParsedGoFiles(absRoot, fset, parser.AllErrors, func() bool { return len(out) >= limit }, func(parsed parsedGoFile) error {
		if parsed.Err == nil {
			return nil
		}
		appendParseDiagnostics(&out, parsed.Rel, parsed.Err, limit)
		return nil
	})
	return out, err
}

func appendParseDiagnostics(out *[]LSPDiagnosticDTO, rel string, err error, limit int) {
	if list, ok := err.(scanner.ErrorList); ok {
		for _, item := range list {
			if len(*out) >= limit {
				return
			}
			*out = append(*out, LSPDiagnosticDTO{File: rel, Line: item.Pos.Line, Column: item.Pos.Column, Severity: "error", Source: "go/parser", Message: item.Msg})
		}
		return
	}
	if len(*out) < limit {
		*out = append(*out, LSPDiagnosticDTO{File: rel, Severity: "error", Source: "go/parser", Message: err.Error()})
	}
}

func scanGoHover(root string, symbol string) (LSPHoverResponse, error) {
	if strings.TrimSpace(symbol) == "" {
		return LSPHoverResponse{}, fmt.Errorf("symbol이 필요해요")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return LSPHoverResponse{}, err
	}
	fset := token.NewFileSet()
	var found LSPHoverResponse
	err = walkParsedGoFiles(absRoot, fset, parser.ParseComments, func() bool { return found.Found }, func(parsed parsedGoFile) error {
		if parsed.Err != nil {
			return nil
		}
		found = hoverFromFile(fset, parsed.File, parsed.Rel, symbol)
		return nil
	})
	return found, err
}

func hoverFromFile(fset *token.FileSet, file *ast.File, rel string, symbol string) LSPHoverResponse {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			container := ""
			kind := "function"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				container = receiverName(d.Recv.List[0].Type)
				kind = "method"
			}
			if !matchesLSPSymbol(symbol, d.Name.Name, container) {
				continue
			}
			p := fset.Position(d.Name.Pos())
			return LSPHoverResponse{Found: true, Symbol: d.Name.Name, Kind: kind, File: rel, Line: p.Line, Column: p.Column, Container: container, Signature: funcSignature(fset, d), Documentation: docText(d.Doc)}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !matchesLSPSymbol(symbol, s.Name.Name, "") {
						continue
					}
					p := fset.Position(s.Name.Pos())
					return LSPHoverResponse{Found: true, Symbol: s.Name.Name, Kind: "type", File: rel, Line: p.Line, Column: p.Column, Signature: "type " + s.Name.Name + " " + formatNode(fset, s.Type), Documentation: firstDocText(s.Doc, d.Doc)}
				case *ast.ValueSpec:
					kind := strings.ToLower(d.Tok.String())
					for _, name := range s.Names {
						if !matchesLSPSymbol(symbol, name.Name, "") {
							continue
						}
						p := fset.Position(name.Pos())
						return LSPHoverResponse{Found: true, Symbol: name.Name, Kind: kind, File: rel, Line: p.Line, Column: p.Column, Signature: kind + " " + name.Name, Documentation: firstDocText(s.Doc, d.Doc)}
					}
				}
			}
		}
	}
	return LSPHoverResponse{}
}

func resolveRelativeGoFile(absRoot string, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path는 project_root 기준 상대 경로여야 해요: %s", relPath)
	}
	path := filepath.Clean(filepath.Join(absRoot, relPath))
	if path != absRoot && !strings.HasPrefix(path, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path가 project_root 밖으로 벗어나요: %s", relPath)
	}
	if !strings.HasSuffix(path, ".go") {
		return "", fmt.Errorf("go 파일만 지원해요: %s", relPath)
	}
	return path, nil
}

func funcSignature(fset *token.FileSet, decl *ast.FuncDecl) string {
	typ := formatNode(fset, decl.Type)
	typ = strings.TrimPrefix(typ, "func")
	if decl.Recv != nil {
		return "func " + receiverSignature(fset, decl.Recv) + " " + decl.Name.Name + typ
	}
	return "func " + decl.Name.Name + typ
}

func receiverSignature(fset *token.FileSet, fields *ast.FieldList) string {
	if fields == nil {
		return "()"
	}
	parts := make([]string, 0, len(fields.List))
	for _, field := range fields.List {
		typ := formatNode(fset, field.Type)
		names := make([]string, 0, len(field.Names))
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
		if len(names) == 0 {
			parts = append(parts, typ)
			continue
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typ)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func formatNode(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

func docText(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	return strings.TrimSpace(group.Text())
}

func firstDocText(groups ...*ast.CommentGroup) string {
	for _, group := range groups {
		if text := docText(group); text != "" {
			return text
		}
	}
	return ""
}

type parsedGoFile struct {
	Path string
	Rel  string
	File *ast.File
	Err  error
}

func walkParsedGoFiles(absRoot string, fset *token.FileSet, mode parser.Mode, stop func() bool, visit func(parsedGoFile) error) error {
	return filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if stop != nil && stop() {
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
		rel, _ := filepath.Rel(absRoot, path)
		file, err := parser.ParseFile(fset, path, nil, mode)
		return visit(parsedGoFile{Path: path, Rel: filepath.ToSlash(rel), File: file, Err: err})
	})
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
