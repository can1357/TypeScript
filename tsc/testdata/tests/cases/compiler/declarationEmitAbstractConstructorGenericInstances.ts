// @declaration: true
// @emitDeclarationOnly: true

// Several instances of a generic class merged with an abstract construct signature type.
type C<T> = abstract new () => T;
export function make<T>() {
    class I { foo!: T; }
    interface I extends C<T> {}
    return null as unknown as I;
}
export const a = make<number>();
export const b = make<string>();
