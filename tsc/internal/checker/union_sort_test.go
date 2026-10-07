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
		choices := []*Type{a, b, b2, z, undefined, never, ab, bz}
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
