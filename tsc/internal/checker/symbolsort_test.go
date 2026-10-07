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

func TestTypeReferencePropertyOrderReuse(t *testing.T) {
	file := parser.ParseSourceFile(ast.SourceFileParseOptions{}, "interface Base { inherited: string; } interface Derived { z: number; a: number; }", core.ScriptKindTS)
	c := &Checker{fileIndexMap: createFileIndexMap([]*ast.SourceFile{file})}
	c.compareSymbols = c.compareSymbolsWorker
	container := newTestSymbol(ast.SymbolFlagsInterface, "", file.Statements.Nodes[1])
	members := make(ast.SymbolTable)
	for _, declaration := range file.Statements.Nodes[1].Members() {
		members[declaration.Name().Text()] = newTestSymbol(ast.SymbolFlagsProperty, declaration.Name().Text(), declaration)
	}
	inheritedDeclaration := file.Statements.Nodes[0].Members()[0]
	members["inherited"] = newTestSymbol(ast.SymbolFlagsProperty, "inherited", inheritedDeclaration)
	target := c.newObjectType(ObjectFlagsInterface|ObjectFlagsReference, container)
	target.AsTypeReference().target = target
	newReference := func(table ast.SymbolTable) *Type {
		result := c.newObjectType(ObjectFlagsReference, container)
		result.AsTypeReference().target = target
		c.setStructuredTypeMembers(result, table, nil, nil, nil)
		return result
	}
	first := newReference(members)
	if !slices.Equal(first.AsStructuredType().properties, c.getNamedMembers(members, container)) {
		t.Fatal("reference property order differs from sorting")
	}
	if target.objectFlags&ObjectFlagsMembersResolved != 0 {
		t.Fatal("seeding an order must not resolve the target")
	}
	if !slices.Equal(c.instantiatedPropertyOrders[target], first.AsStructuredType().properties) {
		t.Fatal("first instantiation did not seed the target order")
	}
	copyMembers := func() ast.SymbolTable {
		result := make(ast.SymbolTable, len(members))
		for name, symbol := range members {
			result[name] = newTestSymbol(symbol.Flags(), name, symbol.Declarations()...)
		}
		return result
	}
	c.symbolSortKeys = nil
	copies := copyMembers()
	reused := newReference(copies)
	for i, property := range first.AsStructuredType().properties {
		if reused.AsStructuredType().properties[i] != copies[property.Name()] {
			t.Fatal("reused order must select the instantiated symbols in source order")
		}
	}
	if c.symbolSortKeys != nil {
		t.Fatal("matching instantiations must not sort again")
	}
	for _, change := range []struct {
		name   string
		mutate func(ast.SymbolTable)
	}{
		{"size", func(table ast.SymbolTable) { delete(table, "inherited") }},
		{"name", func(table ast.SymbolTable) {
			table["other"] = table["a"]
			delete(table, "a")
		}},
		{"declaration", func(table ast.SymbolTable) { table["a"].SetDeclarations([]*ast.Node{inheritedDeclaration}) }},
		{"declaration presence", func(table ast.SymbolTable) { table["a"].SetDeclarations(nil) }},
		{"contained partition", func(table ast.SymbolTable) { table["a"].SetValueDeclaration(inheritedDeclaration) }},
		{"non-value", func(table ast.SymbolTable) { table["a"].SetFlags(ast.SymbolFlagsTypeAlias) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			table := copyMembers()
			change.mutate(table)
			if _, ok := c.tryReusePropertyOrder(table, first.AsStructuredType().properties, true); ok {
				t.Fatal("changed member shape must reject the cached order")
			}
			result := newReference(table)
			if !slices.Equal(result.AsStructuredType().properties, c.getNamedMembers(table, container)) {
				t.Fatal("rejected order must fall back to sorting")
			}
		})
	}
	c.setStructuredTypeMembers(target, members, nil, nil, nil)
	delete(c.instantiatedPropertyOrders, target)
	c.symbolSortKeys = nil
	newReference(copyMembers())
	if c.symbolSortKeys != nil || len(c.instantiatedPropertyOrders) != 0 {
		t.Fatal("a resolved target must supply its own order without sorting or caching")
	}
}

func TestPropertyOrderReuseReturnsCurrentMembers(t *testing.T) {
	c := &Checker{}
	source := newTestSymbol(ast.SymbolFlagsProperty, "property")
	member := newTestSymbol(source.Flags(), source.Name())
	order := []*ast.Symbol{source}
	result, ok := c.tryReusePropertyOrder(ast.SymbolTable{member.Name(): member}, order, false)
	if !ok || len(result) != 1 || result[0] != member {
		t.Fatal("reuse must return the current table's symbol")
	}
	if order[0] != source {
		t.Fatal("reuse must not mutate the cached property order")
	}
	if result, ok := c.tryReusePropertyOrder(ast.SymbolTable{"different": member}, order, false); ok || result != nil {
		t.Fatal("failed validation must not return a partial result")
	}
}

func TestPropertyOrderReuseDistinguishesNilDeclarations(t *testing.T) {
	c := &Checker{}
	source := newTestSymbol(ast.SymbolFlagsProperty, "property", nil)
	member := newTestSymbol(source.Flags(), source.Name())
	if _, ok := c.tryReusePropertyOrder(ast.SymbolTable{member.Name(): member}, []*ast.Symbol{source}, false); ok {
		t.Fatal("a missing declaration and an explicit nil declaration have different comparison ranks")
	}
}
