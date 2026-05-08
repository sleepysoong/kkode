package gateway

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	Symbols         []LSPSymbolDTO `json:"symbols"`
	Limit           int            `json:"limit,omitempty"`
	ResultTruncated bool           `json:"result_truncated,omitempty"`
}

type LSPLocationListResponse struct {
	Locations       []LSPSymbolDTO `json:"locations"`
	Limit           int            `json:"limit,omitempty"`
	ResultTruncated bool           `json:"result_truncated,omitempty"`
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
	References      []LSPReferenceDTO `json:"references"`
	Limit           int               `json:"limit,omitempty"`
	ResultTruncated bool              `json:"result_truncated,omitempty"`
}

type LSPRenameEditDTO struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"end_line"`
	EndColumn int    `json:"end_column"`
	OldText   string `json:"old_text"`
	NewText   string `json:"new_text"`
	Excerpt   string `json:"excerpt,omitempty"`
}

type LSPRenamePreviewResponse struct {
	Symbol          string             `json:"symbol"`
	NewName         string             `json:"new_name"`
	Edits           []LSPRenameEditDTO `json:"edits"`
	Limit           int                `json:"limit,omitempty"`
	ResultTruncated bool               `json:"result_truncated,omitempty"`
}

type LSPFormatPreviewResponse struct {
	File             string `json:"file"`
	Content          string `json:"content"`
	ContentBytes     int    `json:"content_bytes,omitempty"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
	Changed          bool   `json:"changed"`
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
	Diagnostics     []LSPDiagnosticDTO `json:"diagnostics"`
	Limit           int                `json:"limit,omitempty"`
	ResultTruncated bool               `json:"result_truncated,omitempty"`
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
	_, root, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	switch parts[1] {
	case "symbols":
		limit := queryLimit(r, "limit", 200, 1000)
		symbols, err := scanGoSymbols(root, strings.TrimSpace(r.URL.Query().Get("query")), limit+1)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "scan_symbols_failed", err.Error())
			return
		}
		symbols, truncated := limitLSPSymbols(symbols, limit)
		writeJSON(w, LSPSymbolListResponse{Symbols: symbols, Limit: limit, ResultTruncated: truncated})
	case "document-symbols":
		limit := queryLimit(r, "limit", 200, 1000)
		symbols, err := scanGoDocumentSymbols(root, strings.TrimSpace(r.URL.Query().Get("path")), limit+1)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_document_symbols_failed", err.Error())
			return
		}
		symbols, truncated := limitLSPSymbols(symbols, limit)
		writeJSON(w, LSPSymbolListResponse{Symbols: symbols, Limit: limit, ResultTruncated: truncated})
	case "definitions":
		symbol, err := lspSymbolFromQuery(root, r)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_lsp_position", err.Error())
			return
		}
		limit := queryLimit(r, "limit", 50, 200)
		definitions, err := scanGoDefinitions(root, symbol, limit+1)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_definitions_failed", err.Error())
			return
		}
		definitions, truncated := limitLSPSymbols(definitions, limit)
		writeJSON(w, LSPLocationListResponse{Locations: definitions, Limit: limit, ResultTruncated: truncated})
	case "references":
		symbol, err := lspSymbolFromQuery(root, r)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_lsp_position", err.Error())
			return
		}
		limit := queryLimit(r, "limit", 100, 1000)
		references, err := scanGoReferences(root, symbol, limit+1)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_references_failed", err.Error())
			return
		}
		references, truncated := limitLSPReferences(references, limit)
		writeJSON(w, LSPReferenceListResponse{References: references, Limit: limit, ResultTruncated: truncated})
	case "rename-preview":
		symbol, err := lspSymbolFromQuery(root, r)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_lsp_position", err.Error())
			return
		}
		newName := strings.TrimSpace(r.URL.Query().Get("new_name"))
		limit := queryLimit(r, "limit", 1000, 5000)
		preview, err := scanGoRenamePreview(root, symbol, newName, limit+1)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_rename_preview_failed", err.Error())
			return
		}
		edits, truncated := limitLSPRenameEdits(preview.Edits, limit)
		preview.Edits = edits
		preview.Limit = limit
		preview.ResultTruncated = truncated
		writeJSON(w, preview)
	case "format-preview":
		relPath := strings.TrimSpace(r.URL.Query().Get("path"))
		maxBytes := queryLimit(r, "max_bytes", 1<<20, 8<<20)
		preview, err := scanGoFormatPreview(root, relPath, maxBytes)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_format_preview_failed", err.Error())
			return
		}
		writeJSON(w, preview)
	case "diagnostics":
		limit := queryLimit(r, "limit", 200, 1000)
		diagnostics, err := scanGoDiagnostics(root, strings.TrimSpace(r.URL.Query().Get("path")), limit+1)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_diagnostics_failed", err.Error())
			return
		}
		diagnostics, truncated := limitLSPDiagnostics(diagnostics, limit)
		writeJSON(w, LSPDiagnosticListResponse{Diagnostics: diagnostics, Limit: limit, ResultTruncated: truncated})
	case "hover":
		symbol, err := lspSymbolFromQuery(root, r)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_lsp_position", err.Error())
			return
		}
		hover, err := scanGoHover(root, symbol)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "scan_hover_failed", err.Error())
			return
		}
		writeJSON(w, hover)
	default:
		writeError(w, r, http.StatusNotFound, "not_found", "lsp endpoint를 찾을 수 없어요")
	}
}

func lspSymbolFromQuery(root string, r *http.Request) (string, error) {
	if symbol := strings.TrimSpace(r.URL.Query().Get("symbol")); symbol != "" {
		return symbol, nil
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	lineText := strings.TrimSpace(r.URL.Query().Get("line"))
	columnText := strings.TrimSpace(r.URL.Query().Get("column"))
	if relPath == "" && lineText == "" && columnText == "" {
		return "", fmt.Errorf("symbol 또는 path,line,column이 필요해요")
	}
	if relPath == "" || lineText == "" || columnText == "" {
		return "", fmt.Errorf("커서 위치 조회에는 path,line,column이 모두 필요해요")
	}
	line, err := strconv.Atoi(lineText)
	if err != nil || line <= 0 {
		return "", fmt.Errorf("line은 1 이상의 정수여야 해요")
	}
	column, err := strconv.Atoi(columnText)
	if err != nil || column < 0 {
		return "", fmt.Errorf("column은 0 이상의 정수여야 해요")
	}
	if column == 0 {
		column = 1
	}
	return scanGoIdentifierAt(root, relPath, line, column)
}

func scanGoIdentifierAt(root string, relPath string, line int, column int) (string, error) {
	absRoot, err := normalizeProjectRoot(root)
	if err != nil {
		return "", err
	}
	path, err := resolveRelativeGoFile(absRoot, relPath)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return "", err
	}
	var found string
	ast.Inspect(file, func(node ast.Node) bool {
		if found != "" {
			return false
		}
		ident, ok := node.(*ast.Ident)
		if !ok || ident.Name == "_" {
			return true
		}
		start := fset.Position(ident.Pos())
		end := fset.Position(ident.End())
		if start.Line != line || end.Line != line {
			return true
		}
		if column >= start.Column && column < end.Column {
			found = ident.Name
			return false
		}
		return true
	})
	if found == "" {
		return "", fmt.Errorf("커서 위치의 Go 식별자를 찾지 못했어요: %s:%d:%d", relPath, line, column)
	}
	return found, nil
}

func limitLSPSymbols(items []LSPSymbolDTO, limit int) ([]LSPSymbolDTO, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

func limitLSPReferences(items []LSPReferenceDTO, limit int) ([]LSPReferenceDTO, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

func limitLSPRenameEdits(items []LSPRenameEditDTO, limit int) ([]LSPRenameEditDTO, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

func limitLSPDiagnostics(items []LSPDiagnosticDTO, limit int) ([]LSPDiagnosticDTO, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

func scanGoSymbols(root string, query string, limit int) ([]LSPSymbolDTO, error) {
	absRoot, err := normalizeProjectRoot(root)
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

func scanGoDocumentSymbols(root string, relPath string, limit int) ([]LSPSymbolDTO, error) {
	if strings.TrimSpace(relPath) == "" {
		return nil, fmt.Errorf("path가 필요해요")
	}
	if limit <= 0 {
		limit = 200
	}
	absRoot, err := normalizeProjectRoot(root)
	if err != nil {
		return nil, err
	}
	path, err := resolveRelativeGoFile(absRoot, relPath)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(absRoot, path)
	out := []LSPSymbolDTO{}
	appendSymbol := func(name string, kind string, pos token.Pos, container string) {
		if name == "" || len(out) >= limit {
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
	absRoot, err := normalizeProjectRoot(root)
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
	absRoot, err := normalizeProjectRoot(root)
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

func scanGoRenamePreview(root string, symbol string, newName string, limit int) (LSPRenamePreviewResponse, error) {
	symbol = strings.TrimSpace(symbol)
	newName = strings.TrimSpace(newName)
	if symbol == "" {
		return LSPRenamePreviewResponse{}, fmt.Errorf("symbol이 필요해요")
	}
	if !token.IsIdentifier(newName) {
		return LSPRenamePreviewResponse{}, fmt.Errorf("new_name은 Go 식별자여야 해요")
	}
	if limit <= 0 {
		limit = 1000
	}
	refs, err := scanGoReferences(root, symbol, limit)
	if err != nil {
		return LSPRenamePreviewResponse{}, err
	}
	edits := make([]LSPRenameEditDTO, 0, len(refs))
	for _, ref := range refs {
		edits = append(edits, LSPRenameEditDTO{
			File:      ref.File,
			Line:      ref.Line,
			Column:    ref.Column,
			EndLine:   ref.Line,
			EndColumn: ref.Column + len(ref.Name),
			OldText:   ref.Name,
			NewText:   newName,
			Excerpt:   ref.Excerpt,
		})
	}
	return LSPRenamePreviewResponse{Symbol: symbol, NewName: newName, Edits: edits}, nil
}

func scanGoFormatPreview(root string, relPath string, maxBytes int) (LSPFormatPreviewResponse, error) {
	absRoot, err := normalizeProjectRoot(root)
	if err != nil {
		return LSPFormatPreviewResponse{}, err
	}
	path, err := resolveRelativeGoFile(absRoot, relPath)
	if err != nil {
		return LSPFormatPreviewResponse{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LSPFormatPreviewResponse{}, err
	}
	formatted, err := format.Source(data)
	if err != nil {
		return LSPFormatPreviewResponse{}, err
	}
	content, contentBytes, truncated := truncateToolOutput(string(formatted), maxBytes)
	rel, _ := filepath.Rel(absRoot, path)
	return LSPFormatPreviewResponse{File: filepath.ToSlash(rel), Content: content, ContentBytes: contentBytes, ContentTruncated: truncated, Changed: !bytes.Equal(data, formatted)}, nil
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
	absRoot, err := normalizeProjectRoot(root)
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
	absRoot, err := normalizeProjectRoot(root)
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

func normalizeProjectRoot(root string) (string, error) {
	_, absRoot, err := newWorkspace(root)
	if err != nil {
		return "", err
	}
	return absRoot, nil
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
