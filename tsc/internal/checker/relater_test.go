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
		key := relationKey{hash: CacheHashKey{Lo: uint64(i)}, simple: i%2 == 0}
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
		if r.maybeKeysSet.Has(relationKey{hash: CacheHashKey{Lo: uint64(i)}, simple: i%2 == 0}) {
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
	if relation.get(relationKey{}) != RelationComparisonResultNone {
		t.Fatal("empty cache has a result")
	}
	// The same low bits force collisions, including a wrap at the table end.
	for i := range 257 {
		key := relationKey{hash: CacheHashKey{Lo: 15, Hi: uint64(i)}}
		relation.set(key, RelationComparisonResultSucceeded)
	}
	if relation.size() != 257 {
		t.Fatalf("size = %d, want 257", relation.size())
	}
	for i := range 257 {
		key := relationKey{hash: CacheHashKey{Lo: 15, Hi: uint64(i)}}
		if relation.get(key) != RelationComparisonResultSucceeded {
			t.Fatalf("missing key %d after growth", i)
		}
		relation.set(key, RelationComparisonResultFailed|RelationComparisonResultReportsUnreliable)
	}
	if relation.size() != 257 {
		t.Fatalf("replacement changed size to %d", relation.size())
	}
	for i := range 257 {
		key := relationKey{hash: CacheHashKey{Lo: 15, Hi: uint64(i)}}
		if relation.get(key) != RelationComparisonResultFailed|RelationComparisonResultReportsUnreliable {
			t.Fatalf("replacement lost for key %d", i)
		}
	}
	if relation.get(relationKey{hash: CacheHashKey{Lo: 15, Hi: 257}}) != RelationComparisonResultNone {
		t.Fatal("missing colliding key has a result")
	}
}

func TestRelationKey(t *testing.T) {
	c := &Checker{}
	source := c.newObjectType(ObjectFlagsAnonymous, nil)
	target := c.newObjectType(ObjectFlagsAnonymous, nil)
	for _, identity := range []bool{false, true} {
		for _, state := range []IntersectionState{IntersectionStateNone, IntersectionStateSource, IntersectionStateTarget, IntersectionStateSource | IntersectionStateTarget} {
			first, second := target, source
			if identity && first.id > second.id {
				first, second = second, first
			}
			key, constrained := getRelationKey(target, source, state, identity, false)
			want := uint64(first.id)<<33 | uint64(second.id)<<2 | uint64(state)
			if !key.simple || key.hash.Lo != want || key.hash.Hi != 0 || constrained {
				t.Fatal("simple relation key encoding changed")
			}
		}
	}
}

func TestRelationKeyBoundaries(t *testing.T) {
	ids := []TypeId{0, 1, 1<<31 - 1, 1 << 31, 1<<32 - 1}
	states := []IntersectionState{0, 1, 2, 3, 4, 1<<32 - 1}
	for _, sourceID := range ids {
		for _, targetID := range ids {
			for _, state := range states {
				source := &Type{id: sourceID}
				target := &Type{id: targetID}
				key, constrained := getRelationKey(source, target, state, false, false)
				wantSimple := sourceID < 1<<31 && targetID < 1<<31 && state < 4
				if key.simple != wantSimple || constrained {
					t.Fatal("key took the wrong compact/fallback path")
				}
				if wantSimple {
					if TypeId(key.hash.Lo>>33) != sourceID ||
						TypeId(key.hash.Lo>>2&((1<<31)-1)) != targetID ||
						IntersectionState(key.hash.Lo&3) != state || key.hash.Hi != 0 {
						t.Fatal("compact key lost ID/state bits")
					}
				} else {
					var builder keyBuilder
					builder.writeByte('s')
					builder.writeType(source)
					builder.writeType(target)
					builder.writeUint32(uint32(state))
					if key.hash != builder.hash() {
						t.Fatal("fallback hash changed")
					}
				}
			}
		}
	}
}

func TestRelationKeyDomains(t *testing.T) {
	var relation Relation
	for _, packed := range []uint64{0, 1, 1<<64 - 1} {
		simple := relationKey{hash: CacheHashKey{Lo: packed}, simple: true}
		hashed := relationKey{hash: simple.hash}
		relation.set(simple, RelationComparisonResultSucceeded)
		relation.set(hashed, RelationComparisonResultFailed|RelationComparisonResultReportsUnreliable)
		if relation.get(simple) != RelationComparisonResultSucceeded ||
			relation.get(hashed) != RelationComparisonResultFailed|RelationComparisonResultReportsUnreliable {
			t.Fatal("compact and hashed domains aliased")
		}
		relation.set(simple, RelationComparisonResultFailed|RelationComparisonResultComplexityOverflow)
	}
	if relation.size() != 6 {
		t.Fatalf("mixed cache size = %d, want 6", relation.size())
	}
	r := Relater{maybeKeys: []relationKey{{simple: true}}}
	if !r.hasMaybeKey(relationKey{simple: true}) || r.hasMaybeKey(relationKey{}) {
		t.Fatal("maybe stack confused compact zero and zero hash")
	}
}

func TestGenericRelationKeys(t *testing.T) {
	c := &Checker{}
	c.noConstraintType = c.newIntrinsicType(TypeFlagsUnknown, "no constraint")
	target := c.newObjectType(ObjectFlagsInterface|ObjectFlagsReference, nil)
	target.AsTypeReference().target = target
	p := c.newTypeParameter(nil)
	q := c.newTypeParameter(nil)
	p.AsTypeParameter().constraint = c.noConstraintType
	q.AsTypeParameter().constraint = c.noConstraintType
	reference := func(argument *Type) *Type {
		typ := c.newObjectType(ObjectFlagsReference, nil)
		typ.AsTypeReference().target = target
		typ.AsTypeReference().resolvedTypeArguments = []*Type{argument}
		return typ
	}
	pp, qq := reference(p), reference(q)
	pKey, constrained := getRelationKey(pp, pp, IntersectionStateSource, false, false)
	qKey, _ := getRelationKey(qq, qq, IntersectionStateSource, false, false)
	if pKey.simple || qKey.simple || constrained || pKey != qKey {
		t.Fatal("generic parameter renaming equivalence changed")
	}
	var builder keyBuilder
	builder.writeByte('g')
	builder.writeGenericTypeReferences(pp, pp, false)
	builder.writeUint32(uint32(IntersectionStateSource))
	if pKey.hash != builder.hash() {
		t.Fatal("generic relation hash changed")
	}
	q.AsTypeParameter().constraint = c.newIntrinsicType(TypeFlagsNumber, "number")
	constrainedKey, constrained := getRelationKey(pp, qq, IntersectionStateSource, false, false)
	broadest, _ := getRelationKey(pp, qq, IntersectionStateSource, false, true)
	if constrainedKey.simple || !constrained || broadest.simple || constrainedKey == broadest {
		t.Fatal("constrained/broadest generic key distinction changed")
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
		key := relationKey{hash: CacheHashKey{Lo: uint64(i)}, simple: i%2 == 0}
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
		key := relationKey{hash: CacheHashKey{Lo: uint64(i)}, simple: i%2 == 0}
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
	if len(r.maybeKeys) != 0 || r.hasMaybeKey(relationKey{}) {
		t.Fatal("maybe stack not empty after reset")
	}
}
