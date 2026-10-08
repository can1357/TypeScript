//// [tests/cases/compiler/declarationEmitAbstractConstructorGenericInstances.ts] ////

//// [declarationEmitAbstractConstructorGenericInstances.ts]
// Several instances of a generic class merged with an abstract construct signature type.
type C<T> = abstract new () => T;
export function make<T>() {
    class I { foo!: T; }
    interface I extends C<T> {}
    return null as unknown as I;
}
export const a = make<number>();
export const b = make<string>();




//// [declarationEmitAbstractConstructorGenericInstances.d.ts]
export declare function make<T>(): (abstract new () => T) & {
    foo: T;
};
export declare const a: (abstract new () => number) & {
    foo: number;
};
export declare const b: (abstract new () => string) & {
    foo: string;
};
