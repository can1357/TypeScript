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
	layout := c.instantiatedPropertyOrders[target]
	if layout == nil || len(layout.keys) != len(first.AsStructuredType().properties) {
		t.Fatal("first instantiation did not seed the target layout")
	}
	for i, property := range first.AsStructuredType().properties {
		if layout.keys[i] != makeMemberOrderKey(property) {
			t.Fatal("target layout must snapshot the sorted member keys")
		}
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
			if _, ok := c.tryReusePropertyOrder(table, layout, true); ok {
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
	if c.symbolSortKeys != nil || c.instantiatedPropertyOrders[target] == nil {
		t.Fatal("a resolved target must supply its own layout without sorting")
	}
}

func TestPropertyOrderReuseReturnsCurrentMembers(t *testing.T) {
	c := &Checker{}
	source := newTestSymbol(ast.SymbolFlagsProperty, "property")
	member := newTestSymbol(source.Flags(), source.Name())
	order := newMemberLayout([]*ast.Symbol{source})
	result, ok := c.tryReusePropertyOrder(ast.SymbolTable{member.Name(): member}, order, false)
	if !ok || len(result) != 1 || result[0] != member {
		t.Fatal("reuse must return the current table's symbol")
	}
	if order.keys[0] != makeMemberOrderKey(source) {
		t.Fatal("reuse must not mutate the cached property keys")
	}
	if result, ok := c.tryReusePropertyOrder(ast.SymbolTable{"different": member}, order, false); ok || result != nil {
		t.Fatal("failed validation must not return a partial result")
	}
}

func TestPropertyOrderReuseDistinguishesNilDeclarations(t *testing.T) {
	c := &Checker{}
	source := newTestSymbol(ast.SymbolFlagsProperty, "property", nil)
	member := newTestSymbol(source.Flags(), source.Name())
	if _, ok := c.tryReusePropertyOrder(ast.SymbolTable{member.Name(): member}, newMemberLayout([]*ast.Symbol{source}), false); ok {
		t.Fatal("a missing declaration and an explicit nil declaration have different comparison ranks")
	}
}

func TestCompactTypeReferenceMembers(t *testing.T) {
	file := parser.ParseSourceFile(ast.SourceFileParseOptions{}, "interface Base { inherited: string; override: string; } interface Derived { own: number; override: number; }", core.ScriptKindTS)
	c := &Checker{fileIndexMap: createFileIndexMap([]*ast.SourceFile{file})}
	c.couldContainTypeVariables = c.couldContainTypeVariablesWorker
	c.compareSymbols = c.compareSymbolsWorker
	makeMembers := func(declaration *ast.Node) ast.SymbolTable {
		result := make(ast.SymbolTable)
		for _, member := range declaration.Members() {
			result[member.Name().Text()] = newTestSymbol(ast.SymbolFlagsProperty, member.Name().Text(), member)
		}
		return result
	}
	baseSymbol := newTestSymbol(ast.SymbolFlagsInterface, "", file.Statements.Nodes[0])
	baseMembers := makeMembers(baseSymbol.Declarations()[0])
	base := c.newAnonymousType(baseSymbol, baseMembers, nil, nil, nil)
	symbol := newTestSymbol(ast.SymbolFlagsInterface, "", file.Statements.Nodes[1])
	declared := makeMembers(symbol.Declarations()[0])
	declared[ast.InternalSymbolNameCall] = newTestSymbol(0, ast.InternalSymbolNameCall)
	target := c.newObjectType(ObjectFlagsInterface|ObjectFlagsReference, symbol)
	data := target.AsInterfaceType()
	data.target = target
	data.declaredMembersResolved = true
	data.declaredMembers = declared
	data.baseTypesResolved = true
	data.resolvedBaseTypes = []*Type{base}
	parameter := c.newTypeParameter(nil)
	argument := c.newIntrinsicType(TypeFlagsAny, "any")
	resolve := func(wantInstantiated uint32) *Type {
		t.Helper()
		result := c.newObjectType(ObjectFlagsReference, symbol)
		result.AsTypeReference().target = target
		before := c.SymbolCount
		c.resolveObjectTypeMembers(result, target, []*Type{parameter}, []*Type{argument})
		if c.SymbolCount-before != wantInstantiated {
			t.Fatalf("resolution instantiated %d symbols, want %d", c.SymbolCount-before, wantInstantiated)
		}
		return result
	}
	first := resolve(2)
	if first.AsStructuredType().members == nil || first.AsStructuredType().memberLayout != nil {
		t.Fatal("the first resolution must use the map path to establish a canonical layout")
	}
	// Matching instances defer their declared members to first use.
	second := resolve(0)
	resolved := second.AsStructuredType()
	if resolved.members != nil || resolved.memberLayout == nil {
		t.Fatal("matching reference instances must not allocate a member table")
	}
	before := c.SymbolCount
	resolved.Properties()
	resolved.Properties()
	if c.SymbolCount-before != 2 {
		t.Fatalf("first use instantiated %d symbols, want each declared named symbol exactly once", c.SymbolCount-before)
	}
	if resolved.member("inherited") != baseMembers["inherited"] || resolved.member("override") == baseMembers["override"] {
		t.Fatal("inherited members and declared overrides must be selected exactly as before")
	}
	if resolved.member(ast.InternalSymbolNameCall) != nil || resolved.member("absent") != nil {
		t.Fatal("reserved and absent names must remain absent")
	}
	for i, property := range first.AsStructuredType().properties {
		member := resolved.properties[i]
		if member.Name() != property.Name() || core.FirstOrNil(member.Declarations()) != core.FirstOrNil(property.Declarations()) || resolved.member(member.Name()) != member {
			t.Fatal("layout lookup and property order must select the instantiated symbols")
		}
		if c.getPropertyOfObjectType(second, member.Name()) != member {
			t.Fatal("object property lookup must support compact member storage")
		}
	}
	resolved.signatures = []*Signature{{}}
	withoutSignatures := c.getTypeWithoutSignatures(second).AsStructuredType()
	if resolved.memberLayout != nil || withoutSignatures.memberLayout != nil {
		t.Fatal("a table-sharing clone must materialize the table")
	}
	table := resolved.memberTable()
	added := newTestSymbol(ast.SymbolFlagsProperty, "added")
	table[added.Name()] = added
	if resolved.member("added") != added || withoutSignatures.member("added") != added || resolved.memberTable()["added"] != added {
		t.Fatal("materialized table aliases must preserve lookup and sharing semantics")
	}
	baseMembers["new"] = newTestSymbol(ast.SymbolFlagsProperty, "new")
	c.setStructuredTypeMembers(base, baseMembers, nil, nil, nil)
	changed := resolve(2).AsStructuredType()
	if changed.memberLayout != nil || changed.members == nil || changed.member("new") != baseMembers["new"] {
		t.Fatal("a new inherited name must fall back to an exact member table")
	}
	delete(baseMembers, "new")
	oldInherited := baseMembers["inherited"]
	baseMembers["inherited"] = newTestSymbol(ast.SymbolFlagsProperty, oldInherited.Name(), declared["own"].Declarations()...)
	baseMembers["inherited"].SetValueDeclaration(oldInherited.ValueDeclaration())
	c.setStructuredTypeMembers(base, baseMembers, nil, nil, nil)
	changed = resolve(2).AsStructuredType()
	c.resolveObjectTypeMembers(target, target, []*Type{parameter}, []*Type{parameter})
	if target.AsStructuredType().memberLayout != nil || target.AsStructuredType().member(ast.InternalSymbolNameCall) != declared[ast.InternalSymbolNameCall] {
		t.Fatal("non-instantiated targets must retain their full original member table")
	}
	if changed.memberLayout != nil || changed.members == nil || changed.member("inherited") != baseMembers["inherited"] {
		t.Fatal("changed inherited declaration keys must fall back without re-instantiation")
	}
}

func TestMemberLayoutBuilderInheritedSelection(t *testing.T) {
	c := &Checker{}
	nonValue := newTestSymbol(ast.SymbolFlagsTypeAlias, "overridden")
	value := newTestSymbol(ast.SymbolFlagsProperty, "overridden")
	original := newTestSymbol(ast.SymbolFlagsProperty, "own")
	ignored := newTestSymbol(ast.SymbolFlagsProperty, original.Name())
	inherited := newTestSymbol(ast.SymbolFlagsProperty, "inherited")
	canonical := []*ast.Symbol{value, original, inherited}
	layout := newMemberLayout(canonical)
	builder := memberTableBuilder{layout: layout, properties: make([]*ast.Symbol, len(canonical))}
	builder.set(nonValue.Name(), nonValue)
	builder.set(original.Name(), original)
	builder.addInheritedMembers([]*ast.Symbol{value, ignored, inherited})
	if !c.membersMatchLayout(&builder, false) || builder.members != nil ||
		builder.member(value.Name()) != value || builder.member(original.Name()) != original || builder.member(inherited.Name()) != inherited {
		t.Fatal("layout inheritance must replace non-values, retain declared values, and add missing members")
	}
	if builder.member("absent") != nil {
		t.Fatal("an absent layout name must not select index zero")
	}
	builder.properties[0] = newTestSymbol(ast.SymbolFlagsAlias, value.Name())
	if c.membersMatchLayout(&builder, false) {
		t.Fatal("alias-only values must preserve map-path resolution timing")
	}
	builder.properties[0] = nil
	if c.membersMatchLayout(&builder, false) {
		t.Fatal("a missing layout slot must reject compaction")
	}
	table := builder.memberTable()
	if len(table) != 2 || table[original.Name()] != original || table[inherited.Name()] != inherited {
		t.Fatal("failed validation must materialize only members actually present")
	}
	if newMemberLayout([]*ast.Symbol{value, value}) != nil {
		t.Fatal("duplicate canonical names cannot define an exact layout")
	}
}
