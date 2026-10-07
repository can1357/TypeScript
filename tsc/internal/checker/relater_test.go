package checker

import "testing"

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
