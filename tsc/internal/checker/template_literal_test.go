package checker

import "testing"

func TestTemplateLiteralCanonicalCache(t *testing.T) {
	c := &Checker{templateLiteralTypes: make(map[CacheHashKey]*Type)}
	c.stringType = c.newIntrinsicType(TypeFlagsString, "string")
	c.numberType = c.newIntrinsicType(TypeFlagsNumber, "number")
	c.wildcardType = c.newIntrinsicType(TypeFlagsAny, "wildcard")
	parameter := c.newTypeParameter(nil)
	literal := c.newLiteralType(TypeFlagsStringLiteral, "L", nil)
	nested := c.getTemplateLiteralType([]string{"i", "j"}, []*Type{c.numberType})

	for _, test := range []struct {
		name           string
		texts          []string
		spans          []*Type
		canonicalTexts []string
		canonicalSpans []*Type
	}{
		{"pattern", []string{"a", "b"}, []*Type{c.numberType}, []string{"a", "b"}, []*Type{c.numberType}},
		{"generic", []string{"a", "b", "c"}, []*Type{parameter, c.numberType}, []string{"a", "b", "c"}, []*Type{parameter, c.numberType}},
		{"nested", []string{"x", "y"}, []*Type{nested}, []string{"xi", "jy"}, []*Type{c.numberType}},
		{"literal", []string{"a", "b", "c"}, []*Type{literal, c.numberType}, []string{"aLb", "c"}, []*Type{c.numberType}},
		{"surrogates", []string{"\xed\xa0\xbd\xed\xb8\x80", ""}, []*Type{c.numberType}, []string{"\U0001F600", ""}, []*Type{c.numberType}},
	} {
		t.Run(test.name, func(t *testing.T) {
			canonical := c.getTemplateLiteralType(test.canonicalTexts, test.canonicalSpans)
			count := c.TypeCount
			cacheSize := len(c.templateLiteralTypes)
			for range 2 {
				if actual := c.getTemplateLiteralType(test.texts, test.spans); actual != canonical {
					t.Fatalf("got %p, want canonical type %p", actual, canonical)
				}
			}
			if c.TypeCount != count || len(c.templateLiteralTypes) != cacheSize {
				t.Fatal("normalization of cached inputs created additional types or cache entries")
			}
		})
	}
	if actual := c.getTemplateLiteralType([]string{"a", "b"}, []*Type{c.wildcardType}); actual != c.wildcardType {
		t.Fatal("wildcard propagation changed")
	}
}
