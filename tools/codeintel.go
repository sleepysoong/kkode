package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/workspace"
)

type codeIntelSymbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Container string `json:"container,omitempty"`
}

type codeIntelSymbolList struct {
	Symbols         []codeIntelSymbol `json:"symbols,omitempty"`
	Locations       []codeIntelSymbol `json:"locations,omitempty"`
	Limit           int               `json:"limit,omitempty"`
	ResultTruncated bool              `json:"result_truncated,omitempty"`
}

type codeIntelReference struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Excerpt string `json:"excerpt,omitempty"`
}

type codeIntelReferenceList struct {
	References      []codeIntelReference `json:"references"`
	Limit           int                  `json:"limit,omitempty"`
	ResultTruncated bool                 `json:"result_truncated,omitempty"`
}

type codeIntelDiagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type codeIntelDiagnosticList struct {
	Diagnostics     []codeIntelDiagnostic `json:"diagnostics"`
	Limit           int                   `json:"limit,omitempty"`
	ResultTruncated bool                  `json:"result_truncated,omitempty"`
}

type codeIntelHover struct {
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

const maxCodeIntelFileBytes = workspace.MaxFileReadBytes

var errCodeIntelFileTooLarge = errors.New("codeintel Go file is too large")

// CodeIntelTools exposes parser-backed, read-only Go code navigation tools to agent runs.
func CodeIntelTools(ws *workspace.Workspace) ([]llm.Tool, llm.ToolRegistry) {
	strict := true
	cursorOrSymbol := map[string]any{"symbol": StringSchema(), "path": StringSchema(), "line": NonNegativeIntegerSchema(), "column": NonNegativeIntegerSchema(), "limit": NonNegativeIntegerSchema()}
	defs := []llm.Tool{
		{Kind: llm.ToolFunction, Name: "lsp_symbols", Description: "Go workspace symbol 목록을 검색해요", Strict: &strict, Parameters: ObjectSchemaRequired(map[string]any{"query": StringSchema(), "limit": NonNegativeIntegerSchema()}, nil)},
		{Kind: llm.ToolFunction, Name: "lsp_document_symbols", Description: "Go 파일 하나의 symbol outline을 반환해요", Strict: &strict, Parameters: ObjectSchemaRequired(map[string]any{"path": StringSchema()}, []string{"path"})},
		{Kind: llm.ToolFunction, Name: "lsp_definitions", Description: "Go symbol 또는 cursor 위치의 definition을 찾아요", Strict: &strict, Parameters: ObjectSchemaRequired(cursorOrSymbol, nil)},
		{Kind: llm.ToolFunction, Name: "lsp_references", Description: "Go symbol 또는 cursor 위치의 reference를 찾아요", Strict: &strict, Parameters: ObjectSchemaRequired(cursorOrSymbol, nil)},
		{Kind: llm.ToolFunction, Name: "lsp_hover", Description: "Go symbol 또는 cursor 위치의 signature와 doc comment를 반환해요", Strict: &strict, Parameters: ObjectSchemaRequired(cursorOrSymbol, nil)},
		{Kind: llm.ToolFunction, Name: "lsp_diagnostics", Description: "Go parser diagnostics를 반환해요", Strict: &strict, Parameters: ObjectSchemaRequired(map[string]any{"path": StringSchema(), "limit": NonNegativeIntegerSchema()}, nil)},
	}
	handlers := llm.ToolRegistry{
		"lsp_symbols": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			limit, err := codeIntelLimit(in.Limit, 200)
			if err != nil {
				return "", err
			}
			symbols, err := codeIntelSymbols(ws.Root, in.Query, limit+1)
			if err != nil {
				return "", err
			}
			symbols, truncated := limitCodeIntelSymbols(symbols, limit)
			return marshalCodeIntel(codeIntelSymbolList{Symbols: symbols, Limit: limit, ResultTruncated: truncated})
		}),
		"lsp_document_symbols": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path string `json:"path"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			symbols, err := codeIntelDocumentSymbols(ws, in.Path)
			if err != nil {
				return "", err
			}
			return marshalCodeIntel(codeIntelSymbolList{Symbols: symbols})
		}),
		"lsp_definitions": llm.JSONToolHandler(func(ctx context.Context, in codeIntelCursorArgs) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			symbol, err := codeIntelSymbolFromArgs(ws, in)
			if err != nil {
				return "", err
			}
			limit, err := codeIntelLimit(in.Limit, 50)
			if err != nil {
				return "", err
			}
			locations, err := codeIntelDefinitions(ws.Root, symbol, limit+1)
			if err != nil {
				return "", err
			}
			locations, truncated := limitCodeIntelSymbols(locations, limit)
			return marshalCodeIntel(codeIntelSymbolList{Locations: locations, Limit: limit, ResultTruncated: truncated})
		}),
		"lsp_references": llm.JSONToolHandler(func(ctx context.Context, in codeIntelCursorArgs) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			symbol, err := codeIntelSymbolFromArgs(ws, in)
			if err != nil {
				return "", err
			}
			limit, err := codeIntelLimit(in.Limit, 100)
			if err != nil {
				return "", err
			}
			references, err := codeIntelReferences(ws.Root, symbol, limit+1)
			if err != nil {
				return "", err
			}
			references, truncated := limitCodeIntelReferences(references, limit)
			return marshalCodeIntel(codeIntelReferenceList{References: references, Limit: limit, ResultTruncated: truncated})
		}),
		"lsp_hover": llm.JSONToolHandler(func(ctx context.Context, in codeIntelCursorArgs) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			symbol, err := codeIntelSymbolFromArgs(ws, in)
			if err != nil {
				return "", err
			}
			hover, err := codeIntelHoverForSymbol(ws.Root, symbol)
			if err != nil {
				return "", err
			}
			return marshalCodeIntel(hover)
		}),
		"lsp_diagnostics": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path  string `json:"path"`
			Limit int    `json:"limit"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			limit, err := codeIntelLimit(in.Limit, 200)
			if err != nil {
				return "", err
			}
			diagnostics, err := codeIntelDiagnostics(ws, in.Path, limit+1)
			if err != nil {
				return "", err
			}
			diagnostics, truncated := limitCodeIntelDiagnostics(diagnostics, limit)
			return marshalCodeIntel(codeIntelDiagnosticList{Diagnostics: diagnostics, Limit: limit, ResultTruncated: truncated})
		}),
	}
	return defs, handlers
}

type codeIntelCursorArgs struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Limit  int    `json:"limit"`
}

func codeIntelSymbols(root string, query string, limit int) ([]codeIntelSymbol, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	fset := token.NewFileSet()
	out := []codeIntelSymbol{}
	err := walkCodeIntelGoFiles(root, fset, parser.ParseComments, func() bool { return limit > 0 && len(out) >= limit }, func(parsed parsedCodeIntelFile) error {
		if parsed.Err != nil {
			return nil
		}
		ast.Inspect(parsed.File, func(node ast.Node) bool {
			name, kind, container, ok := codeIntelNodeSymbol(node)
			if !ok {
				return true
			}
			if query != "" && !strings.Contains(strings.ToLower(name), query) && !strings.Contains(strings.ToLower(container), query) {
				return true
			}
			p := fset.Position(node.Pos())
			out = append(out, codeIntelSymbol{Name: name, Kind: kind, File: parsed.Rel, Line: p.Line, Column: p.Column, Container: container})
			return len(out) < limit
		})
		return nil
	})
	return out, err
}

func codeIntelDocumentSymbols(ws *workspace.Workspace, relPath string) ([]codeIntelSymbol, error) {
	path, err := ws.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parseCodeIntelGoFile(fset, path, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(ws.Root, path)
	out := []codeIntelSymbol{}
	ast.Inspect(file, func(node ast.Node) bool {
		name, kind, container, ok := codeIntelNodeSymbol(node)
		if !ok {
			return true
		}
		p := fset.Position(node.Pos())
		out = append(out, codeIntelSymbol{Name: name, Kind: kind, File: filepath.ToSlash(rel), Line: p.Line, Column: p.Column, Container: container})
		return true
	})
	return out, nil
}

func codeIntelDefinitions(root string, symbol string, limit int) ([]codeIntelSymbol, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, errors.New("symbol이 필요해요")
	}
	fset := token.NewFileSet()
	out := []codeIntelSymbol{}
	err := walkCodeIntelGoFiles(root, fset, parser.ParseComments, func() bool { return limit > 0 && len(out) >= limit }, func(parsed parsedCodeIntelFile) error {
		if parsed.Err != nil {
			return nil
		}
		ast.Inspect(parsed.File, func(node ast.Node) bool {
			name, kind, container, ok := codeIntelNodeSymbol(node)
			if !ok || !matchesCodeIntelSymbol(symbol, name, container) {
				return true
			}
			p := fset.Position(node.Pos())
			out = append(out, codeIntelSymbol{Name: name, Kind: kind, File: parsed.Rel, Line: p.Line, Column: p.Column, Container: container})
			return len(out) < limit
		})
		return nil
	})
	return out, err
}

func codeIntelReferences(root string, symbol string, limit int) ([]codeIntelReference, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, errors.New("symbol이 필요해요")
	}
	target := symbol
	if idx := strings.LastIndex(symbol, "."); idx >= 0 && idx+1 < len(symbol) {
		target = symbol[idx+1:]
	}
	fset := token.NewFileSet()
	out := []codeIntelReference{}
	err := walkCodeIntelGoFiles(root, fset, 0, func() bool { return limit > 0 && len(out) >= limit }, func(parsed parsedCodeIntelFile) error {
		if parsed.Err != nil {
			return nil
		}
		data, _ := os.ReadFile(parsed.Path)
		lines := strings.Split(string(data), "\n")
		ast.Inspect(parsed.File, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || ident.Name != target {
				return true
			}
			p := fset.Position(ident.Pos())
			out = append(out, codeIntelReference{Name: ident.Name, Kind: "identifier", File: parsed.Rel, Line: p.Line, Column: p.Column, Excerpt: codeIntelLineExcerpt(lines, p.Line)})
			return len(out) < limit
		})
		return nil
	})
	return out, err
}

func codeIntelDiagnostics(ws *workspace.Workspace, relPath string, limit int) ([]codeIntelDiagnostic, error) {
	fset := token.NewFileSet()
	out := []codeIntelDiagnostic{}
	visit := func(parsed parsedCodeIntelFile) error {
		if parsed.Err != nil {
			appendCodeIntelParseDiagnostics(&out, parsed.Rel, parsed.Err, limit)
		}
		return nil
	}
	if strings.TrimSpace(relPath) != "" {
		path, err := ws.Resolve(relPath)
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(ws.Root, path)
		_, err = parseCodeIntelGoFile(fset, path, parser.AllErrors)
		if errors.Is(err, errCodeIntelFileTooLarge) {
			return nil, err
		}
		return out, appendCodeIntelParseDiagnostics(&out, filepath.ToSlash(rel), err, limit)
	}
	err := walkCodeIntelGoFiles(ws.Root, fset, parser.AllErrors, func() bool { return limit > 0 && len(out) >= limit }, visit)
	return out, err
}

func codeIntelHoverForSymbol(root string, symbol string) (codeIntelHover, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return codeIntelHover{}, errors.New("symbol이 필요해요")
	}
	fset := token.NewFileSet()
	var found codeIntelHover
	err := walkCodeIntelGoFiles(root, fset, parser.ParseComments, func() bool { return found.Found }, func(parsed parsedCodeIntelFile) error {
		if parsed.Err != nil {
			return nil
		}
		found = hoverFromCodeIntelFile(fset, parsed.File, parsed.Rel, symbol)
		return nil
	})
	return found, err
}

func codeIntelSymbolFromArgs(ws *workspace.Workspace, in codeIntelCursorArgs) (string, error) {
	if symbol := strings.TrimSpace(in.Symbol); symbol != "" {
		return symbol, nil
	}
	if strings.TrimSpace(in.Path) == "" && in.Line == 0 && in.Column == 0 {
		return "", errors.New("symbol 또는 path,line,column이 필요해요")
	}
	if strings.TrimSpace(in.Path) == "" || in.Line <= 0 || in.Column < 0 {
		return "", errors.New("커서 위치 조회에는 path,line,column이 모두 필요해요")
	}
	column := in.Column
	if column == 0 {
		column = 1
	}
	return scanCodeIntelIdentifierAt(ws, in.Path, in.Line, column)
}

func scanCodeIntelIdentifierAt(ws *workspace.Workspace, relPath string, line int, column int) (string, error) {
	content, err := ws.ReadFile(relPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return "", fmt.Errorf("line 범위가 잘못됐어요: %d", line)
	}
	runes := []rune(lines[line-1])
	idx := column - 1
	if idx >= len(runes) {
		idx = len(runes) - 1
	}
	if idx < 0 {
		return "", fmt.Errorf("column 범위가 잘못됐어요: %d", column)
	}
	start, end := idx, idx
	for start > 0 && isCodeIntelIdentRune(runes[start-1]) {
		start--
	}
	for end < len(runes) && isCodeIntelIdentRune(runes[end]) {
		end++
	}
	if start == end {
		return "", errors.New("커서 위치에서 Go identifier를 찾지 못했어요")
	}
	return string(runes[start:end]), nil
}

func codeIntelNodeSymbol(node ast.Node) (string, string, string, bool) {
	switch n := node.(type) {
	case *ast.FuncDecl:
		container := ""
		if n.Recv != nil && len(n.Recv.List) > 0 {
			container = codeIntelReceiverName(n.Recv.List[0].Type)
		}
		return n.Name.Name, "function", container, true
	case *ast.TypeSpec:
		return n.Name.Name, "type", "", true
	case *ast.ValueSpec:
		if len(n.Names) == 0 {
			return "", "", "", false
		}
		kind := "var"
		if len(n.Values) > 0 {
			kind = "const_or_var"
		}
		return n.Names[0].Name, kind, "", true
	default:
		return "", "", "", false
	}
}

type parsedCodeIntelFile struct {
	Path string
	Rel  string
	File *ast.File
	Err  error
}

func walkCodeIntelGoFiles(absRoot string, fset *token.FileSet, mode parser.Mode, stop func() bool, visit func(parsedCodeIntelFile) error) error {
	return filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if stop != nil && stop() {
			return fs.SkipAll
		}
		if entry.IsDir() {
			if shouldSkipCodeIntelDir(entry.Name()) && path != absRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		rel, _ := filepath.Rel(absRoot, path)
		file, err := parseCodeIntelGoFile(fset, path, mode)
		if errors.Is(err, errCodeIntelFileTooLarge) {
			return nil
		}
		return visit(parsedCodeIntelFile{Path: path, Rel: filepath.ToSlash(rel), File: file, Err: err})
	})
}

func parseCodeIntelGoFile(fset *token.FileSet, path string, mode parser.Mode) (*ast.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxCodeIntelFileBytes {
		return nil, fmt.Errorf("%w: max_bytes=%d", errCodeIntelFileTooLarge, maxCodeIntelFileBytes)
	}
	return parser.ParseFile(fset, path, nil, mode)
}

func shouldSkipCodeIntelDir(name string) bool {
	switch name {
	case ".git", ".kkode", ".omx", ".serena", "node_modules", "vendor", "tmp", "dist", "build", "coverage", "target":
		return true
	default:
		return false
	}
}

func matchesCodeIntelSymbol(symbol string, name string, container string) bool {
	if symbol == name {
		return true
	}
	return container != "" && symbol == container+"."+name
}

func limitCodeIntelSymbols(items []codeIntelSymbol, limit int) ([]codeIntelSymbol, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

func limitCodeIntelReferences(items []codeIntelReference, limit int) ([]codeIntelReference, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

func limitCodeIntelDiagnostics(items []codeIntelDiagnostic, limit int) ([]codeIntelDiagnostic, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

func appendCodeIntelParseDiagnostics(out *[]codeIntelDiagnostic, rel string, err error, limit int) error {
	if err == nil {
		return nil
	}
	if list, ok := err.(scanner.ErrorList); ok {
		for _, item := range list {
			if limit > 0 && len(*out) >= limit {
				break
			}
			*out = append(*out, codeIntelDiagnostic{File: rel, Line: item.Pos.Line, Column: item.Pos.Column, Severity: "error", Source: "go/parser", Message: item.Msg})
		}
		return nil
	}
	if limit <= 0 || len(*out) < limit {
		*out = append(*out, codeIntelDiagnostic{File: rel, Severity: "error", Source: "go/parser", Message: err.Error()})
	}
	return nil
}

func hoverFromCodeIntelFile(fset *token.FileSet, file *ast.File, rel string, symbol string) codeIntelHover {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			container := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				container = codeIntelReceiverName(d.Recv.List[0].Type)
			}
			if !matchesCodeIntelSymbol(symbol, d.Name.Name, container) {
				continue
			}
			p := fset.Position(d.Pos())
			return codeIntelHover{Found: true, Symbol: d.Name.Name, Kind: "function", File: rel, Line: p.Line, Column: p.Column, Container: container, Signature: codeIntelFuncSignature(fset, d), Documentation: codeIntelDocText(d.Doc)}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !matchesCodeIntelSymbol(symbol, s.Name.Name, "") {
						continue
					}
					p := fset.Position(s.Pos())
					return codeIntelHover{Found: true, Symbol: s.Name.Name, Kind: "type", File: rel, Line: p.Line, Column: p.Column, Signature: "type " + s.Name.Name + " " + formatCodeIntelNode(fset, s.Type), Documentation: firstCodeIntelDocText(s.Doc, d.Doc)}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if !matchesCodeIntelSymbol(symbol, name.Name, "") {
							continue
						}
						p := fset.Position(name.Pos())
						return codeIntelHover{Found: true, Symbol: name.Name, Kind: "var", File: rel, Line: p.Line, Column: p.Column, Signature: "var " + name.Name, Documentation: firstCodeIntelDocText(s.Doc, d.Doc)}
					}
				}
			}
		}
	}
	return codeIntelHover{}
}

func codeIntelFuncSignature(fset *token.FileSet, decl *ast.FuncDecl) string {
	typ := formatCodeIntelNode(fset, decl.Type)
	if decl.Recv != nil {
		return "func " + codeIntelReceiverSignature(fset, decl.Recv) + " " + decl.Name.Name + typ
	}
	return "func " + decl.Name.Name + typ
}

func codeIntelReceiverSignature(fset *token.FileSet, fields *ast.FieldList) string {
	if fields == nil {
		return "()"
	}
	parts := make([]string, 0, len(fields.List))
	for _, field := range fields.List {
		typ := formatCodeIntelNode(fset, field.Type)
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

func codeIntelReceiverName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return codeIntelReceiverName(v.X)
	case *ast.IndexExpr:
		return codeIntelReceiverName(v.X)
	case *ast.IndexListExpr:
		return codeIntelReceiverName(v.X)
	case *ast.SelectorExpr:
		return v.Sel.Name
	default:
		return ""
	}
}

func formatCodeIntelNode(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

func codeIntelDocText(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	return strings.TrimSpace(group.Text())
}

func firstCodeIntelDocText(groups ...*ast.CommentGroup) string {
	for _, group := range groups {
		if text := codeIntelDocText(group); text != "" {
			return text
		}
	}
	return ""
}

func codeIntelLineExcerpt(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

func isCodeIntelIdentRune(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}

func codeIntelLimit(value int, fallback int) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("limit must be >= 0")
	}
	if value == 0 {
		return fallback, nil
	}
	return value, nil
}

func marshalCodeIntel(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
