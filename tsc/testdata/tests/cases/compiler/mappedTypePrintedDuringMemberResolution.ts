// @strict: true
// @noEmit: true
// @lib: es5

// While the members of x's mapped type are resolved, errors print it as {}; afterwards, errors
// print its resolved members.
declare function g(s: string): number;
x.a;
declare var x: { [K in "a"]: typeof o };
const o = { v: g(x), w: g(x) };
x.b;
