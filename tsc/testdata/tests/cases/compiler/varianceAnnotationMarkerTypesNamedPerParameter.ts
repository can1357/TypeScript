// @strict: true
// @noEmit: true

// Each invalid variance annotation reports its own marker types (super-U, sub-U, ...), not those
// of the first annotation checked.
type A<in T> = { x: T };
type B<in U> = { x: U };
type C<out V> = (v: V) => void;
type D<out W> = (w: W) => void;
