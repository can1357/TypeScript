package checker

import "testing"

func TestRelaterPoolReset(t *testing.T) {
	c := &Checker{}
	typ := c.newIntrinsicType(TypeFlagsString, "string")
	older := &Relater{c: c}
	c.freeRelater = older
	r := &Relater{
		c: c, relation: &Relation{}, errorChain: &ErrorChain{},
		sourceStack: []*Type{typ}, targetStack: []*Type{typ},
		expandingFlags: ExpandingFlagsBoth, overflow: true, relationCount: 10,
	}
	r.relatedInfo = append(r.relatedInfo, nil)
	for i := range 12 {
		key := CacheHashKey{Lo: uint64(i)}
		r.maybeKeys = append(r.maybeKeys, key)
		if i >= inlineMaybeKeys {
			r.maybeKeysSet.Add(key)
		}
	}
	c.putRelater(r)
	if c.getRelater() != r || c.freeRelater != older || r.c != c {
		t.Fatal("relater pool linkage changed")
	}
	if r.relation != nil || r.errorNode != nil || r.errorChain != nil || r.relatedInfo != nil ||
		len(r.maybeKeys) != 0 || len(r.sourceStack) != 0 || len(r.targetStack) != 0 ||
		r.expandingFlags != ExpandingFlagsNone || r.overflow || r.relationCount != 0 {
		t.Fatal("relater state was not reset")
	}
	for i := inlineMaybeKeys; i < 12; i++ {
		if r.maybeKeysSet.Has(CacheHashKey{Lo: uint64(i)}) {
			t.Fatal("retained maybe key after reset")
		}
	}
}

func TestSimpleRelationFlagPaths(t *testing.T) {
	c := &Checker{
		strictNullChecks:   true,
		assignableRelation: &Relation{}, comparableRelation: &Relation{},
		strictSubtypeRelation: &Relation{},
	}
	anyType := c.newIntrinsicType(TypeFlagsAny, "any")
	unknown := c.newIntrinsicType(TypeFlagsUnknown, "unknown")
	never := c.newIntrinsicType(TypeFlagsNever, "never")
	number := c.newIntrinsicType(TypeFlagsNumber, "number")
	stringType := c.newIntrinsicType(TypeFlagsString, "string")
	object := c.newObjectType(ObjectFlagsAnonymous, nil)
	object.objectFlags |= ObjectFlagsMembersResolved
	fresh := c.newObjectType(ObjectFlagsAnonymous|ObjectFlagsFreshLiteral, nil)
	fresh.objectFlags |= ObjectFlagsMembersResolved
	nonPrimitive := c.newIntrinsicType(TypeFlagsNonPrimitive, "object")
	parameter := c.newTypeParameter(nil)
	undefined := c.newIntrinsicType(TypeFlagsUndefined, "undefined")
	null := c.newIntrinsicType(TypeFlagsNull, "null")
	unknownUnion := c.newType(TypeFlagsUnion, ObjectFlagsNone, &UnionType{
		UnionOrIntersectionType: UnionOrIntersectionType{types: []*Type{undefined, null, object}},
	})
	for i, test := range []struct {
		source, target *Type
		relation       *Relation
		want           bool
	}{
		{anyType, object, c.assignableRelation, true},
		{anyType, never, c.assignableRelation, false},
		{anyType, unknown, c.strictSubtypeRelation, false},
		{object, object, c.assignableRelation, false},
		{object, nonPrimitive, c.assignableRelation, true},
		{object, nonPrimitive, c.strictSubtypeRelation, false},
		{fresh, nonPrimitive, c.strictSubtypeRelation, true},
		{parameter, unknownUnion, c.assignableRelation, true},
		{parameter, unknownUnion, c.strictSubtypeRelation, false},
		{unknown, object, c.assignableRelation, false},
		{number, number, c.assignableRelation, true},
		{stringType, number, c.assignableRelation, false},
		{stringType, unknownUnion, c.comparableRelation, true},
	} {
		if got := c.isSimpleTypeRelatedTo(test.source, test.target, test.relation, nil); got != test.want {
			t.Fatalf("case %d: got %v, want %v", i, got, test.want)
		}
	}
	if unknownUnion.objectFlags&(ObjectFlagsIsUnknownLikeUnionComputed|ObjectFlagsIsUnknownLikeUnion) !=
		ObjectFlagsIsUnknownLikeUnionComputed|ObjectFlagsIsUnknownLikeUnion {
		t.Fatal("unknown-like union classification was not retained")
	}
}

func TestTuplePredicatesAndNormalization(t *testing.T) {
	c := &Checker{}
	object := c.newObjectType(ObjectFlagsAnonymous, nil)
	target := c.newObjectType(ObjectFlagsTuple|ObjectFlagsReference, nil)
	target.AsTypeReference().target = target
	target.AsTupleType().combinedFlags = ElementFlagsVariadic
	reference := c.newObjectType(ObjectFlagsReference, nil)
	reference.AsTypeReference().target = target
	for _, typ := range []*Type{target, reference} {
		if !isTupleType(typ) || !isGenericTupleType(typ) || !c.isGenericTupleType(typ) {
			t.Fatal("generic tuple reference not recognized")
		}
	}
	target.AsTupleType().combinedFlags = ElementFlagsRequired
	if !isTupleType(reference) || isGenericTupleType(reference) || c.isGenericTupleType(reference) ||
		isTupleType(object) || isGenericTupleType(object) {
		t.Fatal("ordinary tuple/object classification changed")
	}
	literal := c.newLiteralType(TypeFlagsStringLiteral, "a", nil)
	fresh := c.newLiteralType(TypeFlagsStringLiteral, "a", literal)
	fresh.AsLiteralType().freshType = fresh
	for _, writing := range []bool{false, true} {
		if c.getNormalizedType(object, writing) != object ||
			c.getNormalizedType(literal, writing) != literal ||
			c.getNormalizedType(fresh, writing) != literal {
			t.Fatal("normalization fast path changed the result")
		}
	}
}

func TestLazyMapperCache(t *testing.T) {
	c := &Checker{}
	c.couldContainTypeVariables = func(typ *Type) bool { return typ.flags&TypeFlagsTypeParameter != 0 }
	outer := c.newTypeParameter(nil)
	inner := c.newTypeParameter(nil)
	number := c.newIntrinsicType(TypeFlagsNumber, "number")
	var mapper *TypeMapper
	mapper = newFunctionTypeMapper(func(typ *Type) *Type {
		if typ == outer {
			return c.instantiateType(inner, mapper)
		}
		return number
	})
	c.pushActiveMapper(mapper)
	if c.activeTypeMappersCaches[0] != nil {
		t.Fatal("empty mapper frame allocated a cache")
	}
	if c.instantiateType(outer, mapper) != number || len(c.activeTypeMappersCaches[0]) != 2 {
		t.Fatal("outer instantiation replaced its recursively allocated cache")
	}
	count := c.TotalInstantiationCount
	if c.instantiateType(inner, mapper) != number || c.TotalInstantiationCount != count {
		t.Fatal("nested instantiation was not cached")
	}
	cache := c.activeTypeMappersCaches[0]
	c.popActiveMapper()
	if len(cache) != 0 || len(c.activeMappers) != 0 {
		t.Fatal("mapper cache not cleared on pop")
	}
	c.pushActiveMapper(mapper)
	if c.activeTypeMappersCaches[0] == nil {
		t.Fatal("pop discarded the reusable cache")
	}
	c.clearActiveMapperCaches()
	c.popActiveMapper()
}

func TestInferencePoolReset(t *testing.T) {
	c := &Checker{}
	typ := c.newTypeParameter(nil)
	n := &InferenceState{
		inferences: []*InferenceInfo{nil}, originalSource: typ, originalTarget: typ,
		priority: 1, inferencePriority: 2, contravariant: true, bivariant: true,
		expandingFlags: ExpandingFlagsBoth, propagationType: typ,
		visited:     map[InferenceKey]InferencePriority{{source: typ.id}: 1},
		sourceStack: []*Type{typ}, targetStack: []*Type{typ},
	}
	c.putInferenceState(n)
	if c.getInferenceState() != n || n.originalSource != nil || n.originalTarget != nil ||
		n.priority != 0 || n.inferencePriority != 0 || n.contravariant || n.bivariant ||
		n.expandingFlags != ExpandingFlagsNone || n.propagationType != nil ||
		len(n.inferences) != 0 || len(n.visited) != 0 || len(n.sourceStack) != 0 || len(n.targetStack) != 0 {
		t.Fatal("inference state was not reset")
	}
	if n.visited == nil {
		t.Fatal("inference reset discarded reusable map")
	}
}

func TestRelationCache(t *testing.T) {
	var relation Relation
	if relation.get(CacheHashKey{}) != RelationComparisonResultNone {
		t.Fatal("empty cache has a result")
	}
	// The same low bits force collisions, including a wrap at the table end.
	for i := range 257 {
		key := CacheHashKey{Lo: 15, Hi: uint64(i)}
		relation.set(key, RelationComparisonResultSucceeded)
	}
	if relation.size() != 257 {
		t.Fatalf("size = %d, want 257", relation.size())
	}
	for i := range 257 {
		key := CacheHashKey{Lo: 15, Hi: uint64(i)}
		if relation.get(key) != RelationComparisonResultSucceeded {
			t.Fatalf("missing key %d after growth", i)
		}
		relation.set(key, RelationComparisonResultFailed|RelationComparisonResultReportsUnreliable)
	}
	if relation.size() != 257 {
		t.Fatalf("replacement changed size to %d", relation.size())
	}
	for i := range 257 {
		key := CacheHashKey{Lo: 15, Hi: uint64(i)}
		if relation.get(key) != RelationComparisonResultFailed|RelationComparisonResultReportsUnreliable {
			t.Fatalf("replacement lost for key %d", i)
		}
	}
	if relation.get(CacheHashKey{Lo: 15, Hi: 257}) != RelationComparisonResultNone {
		t.Fatal("missing colliding key has a result")
	}
}

func TestRelationKey(t *testing.T) {
	c := &Checker{}
	source := c.newObjectType(ObjectFlagsAnonymous, nil)
	target := c.newObjectType(ObjectFlagsAnonymous, nil)
	for _, identity := range []bool{false, true} {
		for _, state := range []IntersectionState{IntersectionStateNone, IntersectionStateSource, IntersectionStateTarget} {
			first, second := target, source
			if identity && first.id > second.id {
				first, second = second, first
			}
			var builder keyBuilder
			builder.writeByte('s')
			builder.writeType(first)
			builder.writeType(second)
			builder.writeUint32(uint32(state))
			key, constrained := getRelationKey(target, source, state, identity, false)
			if key != builder.hash() || constrained {
				t.Fatal("simple relation key changed")
			}
		}
	}
}

func TestGenericReferenceArguments(t *testing.T) {
	c := &Checker{}
	parameter := c.newTypeParameter(nil)
	plain := c.newObjectType(ObjectFlagsAnonymous, nil)
	reference := func(arguments ...*Type) *Type {
		result := c.newObjectType(ObjectFlagsReference, nil)
		result.AsTypeReference().resolvedTypeArguments = arguments
		return result
	}
	concrete := reference(plain)
	generic := reference(parameter)
	nested := reference(generic)
	for _, test := range []struct {
		value *Type
		want  bool
	}{{plain, false}, {concrete, false}, {generic, true}, {nested, true}} {
		for range 2 {
			if got := isTypeReferenceWithGenericArguments(test.value); got != test.want {
				t.Fatalf("generic arguments = %v, want %v", got, test.want)
			}
		}
		if test.value == plain {
			if plain.objectFlags&(ObjectFlagsGenericArgumentsComputed|ObjectFlagsHasGenericArguments) != 0 {
				t.Fatal("non-reference acquired generic argument flags")
			}
		} else if test.value.objectFlags&ObjectFlagsGenericArgumentsComputed == 0 {
			t.Fatal("reference generic argument result not cached")
		}
	}
}
func TestRelaterMaybeKeys(t *testing.T) {
	var relation Relation
	r := Relater{relation: &relation, relationCount: 100}
	for i := range 20 {
		key := CacheHashKey{Lo: uint64(i)}
		r.maybeKeys = append(r.maybeKeys, key)
		if i >= inlineMaybeKeys {
			r.maybeKeysSet.Add(key)
		}
		if !r.hasMaybeKey(key) {
			t.Fatalf("missing maybe key %d", i)
		}
	}
	r.resetMaybeStack(5, RelationComparisonResultReportsUnmeasurable, true)
	for i := range 20 {
		key := CacheHashKey{Lo: uint64(i)}
		if r.hasMaybeKey(key) != (i < 5) {
			t.Fatalf("wrong maybe membership for key %d", i)
		}
		if i >= 5 && relation.get(key) != RelationComparisonResultSucceeded|RelationComparisonResultReportsUnmeasurable {
			t.Fatalf("missing success for key %d", i)
		}
	}
	if r.relationCount != 85 {
		t.Fatalf("relation count = %d, want 85", r.relationCount)
	}
	r.resetMaybeStack(0, RelationComparisonResultNone, false)
	if len(r.maybeKeys) != 0 || r.hasMaybeKey(CacheHashKey{}) {
		t.Fatal("maybe stack not empty after reset")
	}
}
