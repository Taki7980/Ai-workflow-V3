package indexer

import (
	"go/ast"
	"go/parser"
	"go/token"
)

type goASTSymbolParser struct{}

func (goASTSymbolParser) Name() string { return "go-ast" }
func (goASTSymbolParser) Supports(extension string) bool { return extension == ".go" }
func (goASTSymbolParser) Parse(source []byte, relativePath, digest string) ([]Symbol, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, relativePath, source, 0)
	if err != nil {
		return nil, err
	}
	out := []Symbol{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			p := fset.Position(x.Pos())
			e := fset.Position(x.End())
			out = append(out, Symbol{Name: x.Name.Name, Kind: "function", Path: relativePath, Line: p.Line, EndLine: e.Line, SHA256: digest})
		case *ast.TypeSpec:
			p := fset.Position(x.Pos())
			e := fset.Position(x.End())
			out = append(out, Symbol{Name: x.Name.Name, Kind: "type", Path: relativePath, Line: p.Line, EndLine: e.Line, SHA256: digest})
		}
		return true
	})
	return out, nil
}
