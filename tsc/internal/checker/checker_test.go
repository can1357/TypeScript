package checker_test

import (
	"path/filepath"
	"testing"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/bundled"
	"github.com/microsoft/TypeScript/tsc/internal/checker"
	"github.com/microsoft/TypeScript/tsc/internal/compiler"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/repo"
	"github.com/microsoft/TypeScript/tsc/internal/tsoptions"
	"github.com/microsoft/TypeScript/tsc/internal/tspath"
	"github.com/microsoft/TypeScript/tsc/internal/vfs/osvfs"
	"github.com/microsoft/TypeScript/tsc/internal/vfs/vfstest"
	"gotest.tools/v3/assert"
)

func TestTemplateLiteralUnionAlternatives(t *testing.T) {
	t.Parallel()

	fs := bundled.WrapFS(vfstest.FromMap(map[string]string{
		"/main.ts": "type Equal<A, B> = (<T>() => T extends A ? 1 : 2) extends (<T>() => T extends B ? 1 : 2) ? true : false;\n" +
			"type Assert<T extends true> = T;\n" +
			"type Product = `${'a' | 'b'}${1 | 2}`;\n" +
			"type CheckProduct = Assert<Equal<Product, 'a1' | 'a2' | 'b1' | 'b2'>>;\n" +
			"type Nested<T extends string> = `${'x' | 'y'}${`${T}${1 | 2}`}`;\n" +
			"type CheckNested = Assert<Equal<Nested<'a' | 'b'>, 'xa1' | 'xa2' | 'xb1' | 'xb2' | 'ya1' | 'ya2' | 'yb1' | 'yb2'>>;\n" +
			"type CheckPattern = Assert<Equal<Nested<string>, `x${string}1` | `x${string}2` | `y${string}1` | `y${string}2`>>;\n",
		"/tsconfig.json": `{"compilerOptions": {"strict": true}, "files": ["main.ts"]}`,
	}, tspath.CaseInsensitive))

	parsed, errors := tsoptions.GetParsedCommandLineOfConfigFile("/tsconfig.json", &core.CompilerOptions{}, nil, fs, nil)
	assert.Equal(t, len(errors), 0)
	p := compiler.NewProgram(compiler.ProgramOptions{
		Config: parsed,
		Host:   compiler.NewCompilerHost(fs, bundled.LibPath(), nil, nil, nil),
	})
	assert.Equal(t, len(p.GetSemanticDiagnostics(t.Context(), nil)), 0)
}

func TestGetSymbolAtLocation(t *testing.T) {
	t.Parallel()

	content := `interface Foo {
  bar: string;
}
declare const foo: Foo;
foo.bar;`
	fs := vfstest.FromMap(map[string]string{
		"/foo.ts": content,
		"/tsconfig.json": `
				{
					"compilerOptions": {},
					"files": ["foo.ts"]
				}
			`,
	}, tspath.CaseInsensitive /*caseSensitivity*/)
	fs = bundled.WrapFS(fs)

	host := compiler.NewCompilerHost(fs, bundled.LibPath(), nil, nil, nil)
	parsed, errors := tsoptions.GetParsedCommandLineOfConfigFile("/tsconfig.json", &core.CompilerOptions{}, nil, fs, nil)
	assert.Equal(t, len(errors), 0, "Expected no errors in parsed command line")

	p := compiler.NewProgram(compiler.ProgramOptions{
		Config: parsed,
		Host:   host,
	})
	p.BindSourceFiles()
	c, done := p.GetTypeChecker(t.Context())
	defer done()
	file := p.GetSourceFile("/foo.ts")
	interfaceId := file.Statements.Nodes[0].Name()
	varId := file.Statements.Nodes[1].AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].Name()
	propAccess := file.Statements.Nodes[2].Expression()
	nodes := []*ast.Node{interfaceId, varId, propAccess}
	for _, node := range nodes {
		symbol := c.GetSymbolAtLocation(node)
		if symbol == nil {
			t.Fatalf("Expected symbol to be non-nil")
		}
	}
}

func TestGetTypeAtLocationOfTypeOnlyImportClause(t *testing.T) {
	t.Parallel()

	fs := vfstest.FromMap(map[string]string{
		"/types.ts": `export type U = number;
export default interface D { x: number }`,
		"/main.ts": `import type { U } from "./types";
import type * as types from "./types";
import { U as V } from "./types";
import type D from "./types";
export const u: U = 1;
export const v: V = 1;
export type W = types.U;
export type E = D;`,
		"/tsconfig.json": `
				{
					"compilerOptions": {},
					"files": ["types.ts", "main.ts"]
				}
			`,
	}, tspath.CaseInsensitive)
	fs = bundled.WrapFS(fs)

	host := compiler.NewCompilerHost(fs, bundled.LibPath(), nil, nil, nil)

	parsed, errors := tsoptions.GetParsedCommandLineOfConfigFile("/tsconfig.json", &core.CompilerOptions{}, nil, fs, nil)
	assert.Equal(t, len(errors), 0, "Expected no errors in parsed command line")

	p := compiler.NewProgram(compiler.ProgramOptions{
		Config: parsed,
		Host:   host,
	})
	p.BindSourceFiles()
	c, done := p.GetTypeChecker(t.Context())
	defer done()
	file := p.GetSourceFile("/main.ts")
	importClauseAt := func(index int) *ast.Node {
		return file.Statements.Nodes[index].AsImportDeclaration().ImportClause
	}
	// An import clause without a default binding has no symbol of its own. A type-only one
	// should get the same type as the equivalent regular import instead of crashing.
	regular := c.GetTypeAtLocation(importClauseAt(2))
	for _, index := range []int{0, 1} {
		typ := c.GetTypeAtLocation(importClauseAt(index))
		if typ == nil {
			t.Fatalf("Expected type of import clause %d to be non-nil", index)
		}
		assert.Equal(t, typ, regular)
	}

	defaultClause := c.GetTypeAtLocation(importClauseAt(3))
	defaultReference := c.GetTypeAtLocation(file.Statements.Nodes[7].AsTypeAliasDeclaration().Type)
	assert.Equal(t, defaultClause, defaultReference)
}

func TestIntersectionNeverReduction(t *testing.T) {
	t.Parallel()

	fs := bundled.WrapFS(vfstest.FromMap(map[string]string{
		"/main.ts": `
type Unit = { first: string; value: "a"; last: number } & { last: number; value: "b"; first: number };
type Optional = { value?: "a" } & { value?: "b" };
type PresentNever = { value: never } & { value: "a" };
type NonUnit = { value: string } & { value: number };
type ThreeWay = { value: "a" } & { value: "b" } & { other: number };
type Union = ({ value: "a" } | { value: "c" }) & { value: "b" };
class A { private value!: string; }
class B { private value!: string; }
type Private = A & B;
class Base { private value!: string; }
class Derived extends Base {}
type SharedPrivate = Base & Derived;
type Module = typeof import("./other") & { renamed: "b" };
`,
		"/other.ts":      `const value = "a"; export { value as renamed }; export default value;`,
		"/tsconfig.json": `{"compilerOptions":{"strict":true},"files":["main.ts","other.ts"]}`,
	}, tspath.CaseInsensitive))
	host := compiler.NewCompilerHost(fs, bundled.LibPath(), nil, nil, nil)
	parsed, errors := tsoptions.GetParsedCommandLineOfConfigFile("/tsconfig.json", &core.CompilerOptions{}, nil, fs, nil)
	assert.Equal(t, len(errors), 0)
	p := compiler.NewProgram(compiler.ProgramOptions{Config: parsed, Host: host})
	p.BindSourceFiles()
	c, done := p.GetTypeChecker(t.Context())
	defer done()

	wantNever := map[string]bool{
		"Unit":          true,
		"Optional":      false,
		"PresentNever":  false,
		"NonUnit":       false,
		"ThreeWay":      true,
		"Union":         true,
		"Private":       true,
		"SharedPrivate": false,
		"Module":        true,
	}
	for _, statement := range p.GetSourceFile("/main.ts").Statements.Nodes {
		if statement.Kind != ast.KindTypeAliasDeclaration {
			continue
		}
		name := statement.Name().Text()
		t.Run(name, func(t *testing.T) {
			typ := c.GetTypeAtLocation(statement.AsTypeAliasDeclaration().Type)
			assert.Equal(t, c.TypeToString(typ) == "never", wantNever[name])
		})
	}
}

func BenchmarkNewChecker(b *testing.B) {
	fs := bundled.WrapFS(osvfs.FS())
	rootPath := tspath.RootedDirectoryPathFromAbsolute(filepath.Join(repo.TestDataPath(), "fixtures/compiler"))
	host := compiler.NewCompilerHost(fs, bundled.LibPath(), nil, nil, nil)
	parsed, errors := tsoptions.GetParsedCommandLineOfConfigFile(rootPath.ResolveFile("tsconfig.json"), &core.CompilerOptions{}, nil, fs, nil)
	assert.Equal(b, len(errors), 0, "Expected no errors in parsed command line")
	program := compiler.NewProgram(compiler.ProgramOptions{
		Config: parsed,
		Host:   host,
	})

	b.ReportAllocs()
	for b.Loop() {
		checker.NewChecker(program, nil)
	}
}
