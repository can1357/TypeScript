package checker

import (
	"testing"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
)

func TestInstantiateSymbolDoesNotAllocateSourceLinks(t *testing.T) {
	var c Checker
	source := newTestSymbol(ast.SymbolFlagsProperty, "property")
	result := c.instantiateSymbol(source, nil)
	if c.valueSymbolLinks.Has(source) {
		t.Fatal("instantiating an unresolved symbol allocated source links")
	}
	links := c.valueSymbolLinks.TryGet(result)
	if links == nil || links.target != source || links.mapper != nil || links.nameType != nil {
		t.Fatal("instantiation lost its target or zero-valued metadata")
	}
	nameType := &Type{flags: TypeFlagsStringLiteral}
	c.valueSymbolLinks.Get(source).nameType = nameType
	result = c.instantiateSymbol(source, nil)
	if c.valueSymbolLinks.TryGet(result).nameType != nameType {
		t.Fatal("instantiation lost existing name metadata")
	}
}

func TestReadOnlySymbolLinksRemainAbsent(t *testing.T) {
	var c Checker
	symbol := newTestSymbol(ast.SymbolFlagsProperty, "property")
	if c.getNameTypeOfSymbol(symbol) != nil || c.isReferenced(symbol) {
		t.Fatal("missing links did not read as zero values")
	}
	c.symbolReferenced(symbol, 0)
	if c.valueSymbolLinks.Has(symbol) || c.symbolReferenceLinks.Has(symbol) {
		t.Fatal("zero-valued reads or references allocated links")
	}
	c.symbolReferenced(symbol, ast.SymbolFlagsProperty)
	c.symbolReferenced(symbol, ast.SymbolFlagsTypeParameter)
	if c.getSymbolReferenceKinds(symbol) != ast.SymbolFlagsProperty|ast.SymbolFlagsTypeParameter {
		t.Fatal("reference meanings were not accumulated")
	}
}
