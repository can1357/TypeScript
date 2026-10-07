package checker

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/parser"
)

// newTestSymbol returns a binder-style symbol for checker tests that need symbols without a program.
func newTestSymbol(flags ast.SymbolFlags, name string, declarations ...*ast.Node) *ast.Symbol {
	symbol := ast.NewSymbol()
	symbol.SetFlags(flags)
	symbol.SetName(name)
	symbol.SetDeclarations(declarations)
	if len(declarations) == 1 && flags&ast.SymbolFlagsValue != 0 {
		symbol.SetValueDeclaration(declarations[0])
	}
	return symbol
}

func TestSortSymbolsMatchesComparator(t *testing.T) {
	files := []*ast.SourceFile{
		parser.ParseSourceFile(ast.SourceFileParseOptions{}, "interface A { z: string; a: number; }", core.ScriptKindTS),
		parser.ParseSourceFile(ast.SourceFileParseOptions{}, "interface B { x: string; y: number; }", core.ScriptKindTS),
		parser.ParseSourceFile(ast.SourceFileParseOptions{}, "interface C { z: string; a: number; }", core.ScriptKindTS),
	}
	c := &Checker{fileIndexMap: createFileIndexMap(files[:2])}
	c.compareSymbols = c.compareSymbolsWorker
	symbols := []*ast.Symbol{nil, newTestSymbol(0, "duplicate"), newTestSymbol(0, "duplicate"), newTestSymbol(0, "nil", nil)}
	for _, file := range files {
		for _, declaration := range file.Statements.Nodes[0].Members() {
			symbols = append(symbols, newTestSymbol(0, declaration.Name().Text(), declaration))
		}
	}
	for _, symbol := range symbols {
		if symbol != nil {
			ast.GetSymbolId(symbol)
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 100 {
		rng.Shuffle(len(symbols), func(i, j int) { symbols[i], symbols[j] = symbols[j], symbols[i] })
		want := slices.Clone(symbols)
		slices.SortFunc(want, c.compareSymbols)
		got := slices.Clone(symbols)
		c.sortSymbols(got)
		if !slices.Equal(got, want) {
			t.Fatal("cached sort keys changed symbol order")
		}
	}
	for _, key := range c.symbolSortKeys[:cap(c.symbolSortKeys)] {
		if key.symbol != nil || key.file != nil {
			t.Fatal("sort scratch storage retained AST references")
		}
	}
}

func TestAnonymousInstantiationPreservesMemberOrder(t *testing.T) {
	file := parser.ParseSourceFile(ast.SourceFileParseOptions{}, "interface Base { inherited: string; overridden: string; } interface Derived { own: number; overridden: number; }", core.ScriptKindTS)
	c := &Checker{fileIndexMap: createFileIndexMap([]*ast.SourceFile{file})}
	c.compareSymbols = c.compareSymbolsWorker
	container := newTestSymbol(ast.SymbolFlagsInterface, "", file.Statements.Nodes[1])
	members := make(ast.SymbolTable)
	for _, declaration := range container.Declarations()[0].Members() {
		symbol := newTestSymbol(ast.SymbolFlagsProperty, declaration.Name().Text(), declaration)
		members[symbol.Name()] = symbol
	}
	var inherited []*ast.Symbol
	for _, declaration := range file.Statements.Nodes[0].Members() {
		inherited = append(inherited, newTestSymbol(ast.SymbolFlagsProperty, declaration.Name().Text(), declaration))
	}
	members = c.addInheritedMembers(members, inherited)
	source := c.newAnonymousType(container, members, nil, nil, nil)
	instantiated := c.newObjectType(ObjectFlagsAnonymous|ObjectFlagsInstantiated, container)
	instantiated.AsObjectType().target = source
	copies := make(ast.SymbolTable, len(members))
	for name, symbol := range members {
		copies[name] = newTestSymbol(symbol.Flags(), symbol.Name(), symbol.Declarations()...)
	}
	c.setStructuredTypeMembers(instantiated, copies, nil, nil, nil)
	want := c.getNamedMembers(copies, container)
	if !slices.Equal(instantiated.AsStructuredType().properties, want) {
		t.Fatal("instantiation changed the contained/inherited partition or sort order")
	}
	if want[0].Name() != "own" || want[1].Name() != "overridden" || want[2].Name() != "inherited" {
		t.Fatal("explicit members must precede inherited members, with overrides retained")
	}
	c.setStructuredTypeMembers(instantiated, nil, nil, nil, nil)
	reverse := c.newObjectType(ObjectFlagsAnonymous|ObjectFlagsReverseMapped, nil)
	reverse.AsObjectType().target = source
	replacement := newTestSymbol(ast.SymbolFlagsProperty, "replacement")
	c.setStructuredTypeMembers(reverse, ast.SymbolTable{replacement.Name(): replacement}, nil, nil, nil)
	if !slices.Equal(reverse.AsStructuredType().properties, []*ast.Symbol{replacement}) {
		t.Fatal("reverse mapped members must be ordered independently of their target")
	}
	if instantiated.AsStructuredType().properties != nil {
		t.Fatal("temporary empty member resolution must remain empty")
	}
}
