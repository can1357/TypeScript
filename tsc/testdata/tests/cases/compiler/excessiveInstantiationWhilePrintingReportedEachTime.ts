// @noEmit: true

// Printing the type of b.f instantiates its return type too deeply; each print reports it.
declare class B<Q> { f(): [[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[Q]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]; }
function check<U>() {
 let b!: B<[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[U]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]>;
 let x: never = b.f;
 let y: never = b.f;
}
