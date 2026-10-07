package checker

import (
	"slices"
	"testing"
)

func TestAddTypesToUnionOrdering(t *testing.T) {
	for _, strictNullChecks := range []bool{false, true} {
		c := &Checker{strictNullChecks: strictNullChecks}
		a := c.newLiteralType(TypeFlagsStringLiteral, "a", nil)
		b := c.newLiteralType(TypeFlagsStringLiteral, "b", nil)
		b2 := c.newLiteralType(TypeFlagsStringLiteral, "b", nil)
		z := c.newLiteralType(TypeFlagsStringLiteral, "z", nil)
		undefined := c.newIntrinsicType(TypeFlagsUndefined, "undefined")
		never := c.newIntrinsicType(TypeFlagsNever, "never")
		ab := c.newUnionType(ObjectFlagsNone, []*Type{a, b})
		bz := c.newUnionType(ObjectFlagsNone, []*Type{b, z})
		nullableTypes := []*Type{a, undefined}
		slices.SortFunc(nullableTypes, CompareTypes)
		nullable := c.newUnionType(ObjectFlagsNone, nullableTypes)
		named := c.newUnionType(ObjectFlagsNone, []*Type{b2, z})
		named.alias = &TypeAlias{}
		choices := []*Type{a, b, b2, z, undefined, never, ab, bz, nullable, named}
		for _, first := range choices {
			for _, second := range choices {
				for _, third := range choices {
					for _, source := range [][]*Type{{first, second}, {first, second, third}} {
						var expected []*Type
						var includes TypeFlags
						for _, input := range source {
							constituents := []*Type{input}
							if input.flags&TypeFlagsUnion != 0 {
								constituents = input.Types()
								if input.alias != nil || input.AsUnionType().origin != nil {
									includes |= TypeFlagsUnion
								}
							}
							for _, typ := range constituents {
								if typ.flags&TypeFlagsNever != 0 {
									continue
								}
								includes |= typ.flags & TypeFlagsIncludesMask
								if !strictNullChecks && typ.flags&TypeFlagsNullable != 0 {
									includes |= TypeFlagsIncludesNonWideningType
									continue
								}
								expected = append(expected, typ)
							}
						}
						slices.SortStableFunc(expected, CompareTypes)
						expected = slices.Compact(expected)
						actual, actualIncludes := c.addTypesToUnion(source)
						if !slices.Equal(actual, expected) || actualIncludes != includes {
							t.Fatalf("strictNullChecks=%v source=%v: types=%v includes=%v, want types=%v includes=%v", strictNullChecks, source, actual, actualIncludes, expected, includes)
						}
					}
				}
			}
		}
	}
}

func TestContainsTypePointerHits(t *testing.T) {
	c := &Checker{}
	var types []*Type
	for range 10 {
		types = append(types, c.newIntrinsicType(TypeFlagsString, "string"))
	}
	for length := 0; length <= len(types); length++ {
		for _, target := range types {
			actual := containsType(types[:length], target)
			expected := slices.Contains(types[:length], target)
			if actual != expected {
				t.Fatalf("length=%d target=%v: containsType=%v, want %v", length, target, actual, expected)
			}
		}
	}
}
