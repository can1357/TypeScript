package checker

import (
	"strings"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/nodebuilder"
	"github.com/microsoft/TypeScript/tsc/internal/printer"
)

// TODO: Memoize once per checker to retain threadsafety
func createPrinterWithDefaults(emitContext *printer.EmitContext) *printer.Printer {
	return printer.NewPrinter(printer.PrinterOptions{}, printer.PrintHandlers{}, emitContext)
}

func createPrinterWithRemoveComments(emitContext *printer.EmitContext) *printer.Printer {
	return printer.NewPrinter(printer.PrinterOptions{RemoveComments: true}, printer.PrintHandlers{}, emitContext)
}

func createPrinterWithRemoveCommentsOmitTrailingSemicolon(emitContext *printer.EmitContext) *printer.Printer {
	return printer.NewPrinter(printer.PrinterOptions{
		RemoveComments:        true,
		OmitTrailingSemicolon: true,
	}, printer.PrintHandlers{}, emitContext)
}

func createPrinterWithRemoveCommentsOmitTrailingSemicolonNeverAsciiEscape(emitContext *printer.EmitContext) *printer.Printer {
	return printer.NewPrinter(printer.PrinterOptions{
		RemoveComments:        true,
		OmitTrailingSemicolon: true,
		NeverAsciiEscape:      true,
	}, printer.PrintHandlers{}, emitContext)
}

func createPrinterWithRemoveCommentsNeverAsciiEscape(emitContext *printer.EmitContext) *printer.Printer {
	return printer.NewPrinter(printer.PrinterOptions{
		RemoveComments:   true,
		NeverAsciiEscape: true,
	}, printer.PrintHandlers{}, emitContext)
}

func (c *Checker) TypeToString(t *Type) string {
	return c.typeToString(t, nil)
}

func (c *Checker) typeToString(t *Type, enclosingDeclaration *ast.Node) string {
	return c.typeToStringEx(t, enclosingDeclaration, TypeFormatFlagsAllowUniqueESSymbolType|TypeFormatFlagsUseAliasDefinedOutsideCurrentScope, nil)
}

func toNodeBuilderFlags(flags TypeFormatFlags) nodebuilder.Flags {
	return nodebuilder.Flags(flags & TypeFormatFlagsNodeBuilderFlagsMask)
}

func (c *Checker) TypeToStringEx(t *Type, enclosingDeclaration *ast.Node, flags TypeFormatFlags, vc *VerbosityContext) string {
	return c.typeToStringEx(t, enclosingDeclaration, flags, vc)
}

// typeToStringKey identifies a memoizable typeToStringEx call; see typeToStringEx.
type typeToStringKey struct {
	t                    *Type
	enclosingDeclaration *ast.Node
	flags                TypeFormatFlags
	// Variance checks print their marker types using the type parameter under check.
	varianceTypeParameter *Type
}

// typeToStringEntry is a memoized print and the number of type instantiations a repeat of it
// performs. ready is false after the first print, which records nothing: it performs the lazy
// resolutions and instantiations that later prints find cached, so it costs more than a repeat.
// depth is the deepest instantiation depth a print started at without reaching the depth limit;
// a print that starts no deeper does not reach it either.
type typeToStringEntry struct {
	text           string
	instantiations uint32
	depth          int
	ready          bool
}

// maxInstantiationCount is the number of type instantiations caused by one statement or expression
// after which instantiateTypeWithAlias gives up (TS2589).
const maxInstantiationCount = 5_000_000

func (c *Checker) typeToStringEx(t *Type, enclosingDeclaration *ast.Node, flags TypeFormatFlags, vc *VerbosityContext) string {
	// Serialization of types can lead to (lazy) resolution of members, which can cause diagnostics that again require
	// serialization of types. This can potentially result in infinite recursion and stack overflows. To prevent that,
	// after a certain number of recursive invocations the function simply returns "?".
	if c.serializationLevel >= maxSerializationLevel {
		return "?"
	}
	// Relation error elaboration prints the same few types tens of thousands of times, mostly in branches whose
	// errors are discarded. The first print performs, with all of its side effects, every lazy resolution the
	// serialization needs; a repeated print with the same inputs only revisits those cached results and yields the
	// same text. Nested prints are not memoized because they can be cut short at maxSerializationLevel.
	// A repeat must leave the checker as the print would have. So prints are not memoized while resolving members
	// (mapped and anonymous types have empty members until their resolution completes, and print as {} meanwhile),
	// nor when they report a diagnostic (such as TS2589 for a too deep instantiation) or run into a type resolution
	// cycle (which yields degraded text and marks the in-progress resolutions as circular). A print without either
	// leaves everything it depends on resolved, so no later print of the same inputs runs into one. Memoized repeats
	// count the instantiations a repeat performs (measured by the second print), and are recomputed if those would
	// reach the instantiation limit or if they start deeper in instantiations than the prints seen so far.
	memoize := vc == nil && c.serializationLevel == 0 && c.resolvingMembers == 0
	var key typeToStringKey
	var entry typeToStringEntry
	var seen bool
	depth := len(c.instantiationStack)
	if memoize {
		key = typeToStringKey{t, enclosingDeclaration, flags, c.varianceTypeParameter}
		entry, seen = c.typeToStringCache[key]
		if entry.ready && depth <= entry.depth && c.instantiationCount+entry.instantiations < maxInstantiationCount {
			c.instantiationCount += entry.instantiations
			return entry.text
		}
	}
	events, instantiations := c.printSensitiveEvents, c.instantiationCount
	result := c.typeToStringWorker(t, enclosingDeclaration, flags, vc)
	if memoize && c.printSensitiveEvents == events && c.instantiationCount >= instantiations {
		if c.typeToStringCache == nil {
			c.typeToStringCache = make(map[typeToStringKey]typeToStringEntry)
		}
		// The first print only marks the inputs as seen; the second one is memoized.
		c.typeToStringCache[key] = typeToStringEntry{result, c.instantiationCount - instantiations, max(depth, entry.depth), seen}
	}
	return result
}

func (c *Checker) typeToStringWorker(t *Type, enclosingDeclaration *ast.Node, flags TypeFormatFlags, vc *VerbosityContext) string {
	newLine := ""
	if flags&TypeFormatFlagsMultilineObjectLiterals != 0 {
		newLine = "\n"
	}
	writer := printer.NewTextWriter(newLine, 0)
	noTruncation := ((vc == nil || vc.MaxTruncationLength == 0) && c.compilerOptions.NoErrorTruncation == core.TSTrue) || (flags&TypeFormatFlagsNoTruncation != 0)
	combinedFlags := toNodeBuilderFlags(flags) | nodebuilder.FlagsIgnoreErrors
	if noTruncation {
		combinedFlags = combinedFlags | nodebuilder.FlagsNoTruncation
	}
	nodeBuilder, release := c.getNodeBuilder()
	defer release()
	oldVerbosity := nodeBuilder.verbosity
	nodeBuilder.verbosity = vc
	defer func() {
		nodeBuilder.verbosity = oldVerbosity
	}()
	c.serializationLevel++
	typeNode := nodeBuilder.TypeToTypeNode(t, enclosingDeclaration, combinedFlags, nodebuilder.InternalFlagsNone, nil)
	c.serializationLevel--
	if typeNode == nil {
		panic("should always get typenode")
	}
	// The unresolved type gets a synthesized comment on `any` to hint to users that it's not a plain `any`.
	// Otherwise, we always strip comments out.
	var p *printer.Printer
	if t == c.unresolvedType {
		p = createPrinterWithDefaults(nodeBuilder.EmitContext())
	} else {
		p = createPrinterWithRemoveComments(nodeBuilder.EmitContext())
	}
	var sourceFile *ast.SourceFile
	if enclosingDeclaration != nil {
		sourceFile = ast.GetSourceFileOfNode(enclosingDeclaration)
	}
	p.Write(typeNode, sourceFile, writer, nil)
	result := writer.String()

	maxLength := defaultMaximumTruncationLength * 2
	if vc != nil && vc.MaxTruncationLength > 0 {
		maxLength = vc.MaxTruncationLength * 10 // hard cutoff matching Strada's absoluteMaximumLength
	}
	if noTruncation {
		maxLength = noTruncationMaximumTruncationLength * 2
	}
	if maxLength > 0 && result != "" && len(result) >= maxLength {
		if vc != nil {
			vc.Truncated = true
		}
		return result[0:maxLength-len("...")] + "..."
	}
	return result
}

func (c *Checker) SymbolToString(s *ast.Symbol) string {
	return c.symbolToString(s)
}

func (c *Checker) symbolToString(symbol *ast.Symbol) string {
	return c.symbolToStringEx(symbol, nil, ast.SymbolFlagsAll, SymbolFormatFlagsAllowAnyNodeKind)
}

func (c *Checker) SymbolToStringEx(symbol *ast.Symbol, enclosingDeclaration *ast.Node, meaning ast.SymbolFlags, flags SymbolFormatFlags) string {
	return c.symbolToStringEx(symbol, enclosingDeclaration, meaning, flags)
}

func (c *Checker) symbolToStringEx(symbol *ast.Symbol, enclosingDeclaration *ast.Node, meaning ast.SymbolFlags, flags SymbolFormatFlags) string {
	writer, putWriter := printer.GetSingleLineStringWriter()
	defer putWriter()

	nodeFlags := nodebuilder.FlagsIgnoreErrors
	internalNodeFlags := nodebuilder.InternalFlagsNone
	if flags&SymbolFormatFlagsUseOnlyExternalAliasing != 0 {
		nodeFlags |= nodebuilder.FlagsUseOnlyExternalAliasing
	}
	if flags&SymbolFormatFlagsWriteTypeParametersOrArguments != 0 {
		nodeFlags |= nodebuilder.FlagsWriteTypeParametersInQualifiedName
	}
	if flags&SymbolFormatFlagsUseAliasDefinedOutsideCurrentScope != 0 {
		nodeFlags |= nodebuilder.FlagsUseAliasDefinedOutsideCurrentScope
	}
	if flags&SymbolFormatFlagsDoNotIncludeSymbolChain != 0 {
		internalNodeFlags |= nodebuilder.InternalFlagsDoNotIncludeSymbolChain
	}
	if flags&SymbolFormatFlagsWriteComputedProps != 0 {
		internalNodeFlags |= nodebuilder.InternalFlagsWriteComputedProps
	}

	nodeBuilder, release := c.getNodeBuilder()
	defer release()
	var sourceFile *ast.SourceFile
	if enclosingDeclaration != nil {
		sourceFile = ast.GetSourceFileOfNode(enclosingDeclaration)
	}
	var printer_ *printer.Printer
	// add neverAsciiEscape for GH#39027
	if enclosingDeclaration != nil && enclosingDeclaration.Kind == ast.KindSourceFile {
		printer_ = createPrinterWithRemoveCommentsOmitTrailingSemicolonNeverAsciiEscape(nodeBuilder.EmitContext())
	} else {
		printer_ = createPrinterWithRemoveCommentsOmitTrailingSemicolon(nodeBuilder.EmitContext())
	}

	var builder func(symbol *ast.Symbol, meaning ast.SymbolFlags, enclosingDeclaration *ast.Node, flags nodebuilder.Flags, internalFlags nodebuilder.InternalFlags, tracker nodebuilder.SymbolTracker) *ast.Node
	if flags&SymbolFormatFlagsAllowAnyNodeKind != 0 {
		builder = nodeBuilder.SymbolToNode
	} else {
		builder = nodeBuilder.SymbolToEntityName
	}
	entity := builder(symbol, meaning, enclosingDeclaration, nodeFlags, internalNodeFlags, nil) // TODO: GH#18217
	printer_.Write(entity /*sourceFile*/, sourceFile, writer, nil)                              // TODO: GH#18217
	return writer.String()
}

func (c *Checker) signatureToString(signature *Signature) string {
	return c.signatureToStringEx(signature, nil, TypeFormatFlagsNone, nil)
}

func (c *Checker) SignatureToStringEx(signature *Signature, enclosingDeclaration *ast.Node, flags TypeFormatFlags, vc *VerbosityContext) string {
	return c.signatureToStringEx(signature, enclosingDeclaration, flags, vc)
}

func (c *Checker) signatureToStringEx(signature *Signature, enclosingDeclaration *ast.Node, flags TypeFormatFlags, vc *VerbosityContext) string {
	isConstructor := signature.flags&SignatureFlagsConstruct != 0 && flags&TypeFormatFlagsWriteCallStyleSignature == 0
	var sigOutput ast.Kind
	if flags&TypeFormatFlagsWriteArrowStyleSignature != 0 {
		if isConstructor {
			sigOutput = ast.KindConstructorType
		} else {
			sigOutput = ast.KindFunctionType
		}
	} else {
		if isConstructor {
			sigOutput = ast.KindConstructSignature
		} else {
			sigOutput = ast.KindCallSignature
		}
	}

	nodeBuilder, release := c.getNodeBuilder()
	defer release()
	oldVerbosity := nodeBuilder.verbosity
	nodeBuilder.verbosity = vc
	defer func() {
		nodeBuilder.verbosity = oldVerbosity
	}()
	combinedFlags := toNodeBuilderFlags(flags) | nodebuilder.FlagsIgnoreErrors | nodebuilder.FlagsWriteTypeParametersInQualifiedName
	sig := nodeBuilder.SignatureToSignatureDeclaration(signature, sigOutput, enclosingDeclaration, combinedFlags, nodebuilder.InternalFlagsNone, nil)
	p := createPrinterWithRemoveCommentsOmitTrailingSemicolonNeverAsciiEscape(nodeBuilder.EmitContext())
	var sourceFile *ast.SourceFile
	if enclosingDeclaration != nil {
		sourceFile = ast.GetSourceFileOfNode(enclosingDeclaration)
	}
	if flags&TypeFormatFlagsMultilineObjectLiterals != 0 {
		writer := printer.NewTextWriter("\n", 0)
		p.Write(sig, sourceFile, writer, nil)
		return writer.String()
	}
	writer, putWriter := printer.GetSingleLineStringWriter()
	defer putWriter()
	p.Write(sig, sourceFile, writer, nil)
	return writer.String()
}

func (c *Checker) typePredicateToString(typePredicate *TypePredicate) string {
	return c.typePredicateToStringEx(typePredicate, nil, TypeFormatFlagsUseAliasDefinedOutsideCurrentScope)
}

func (c *Checker) typePredicateToStringEx(typePredicate *TypePredicate, enclosingDeclaration *ast.Node, flags TypeFormatFlags) string {
	writer, putWriter := printer.GetSingleLineStringWriter()
	defer putWriter()
	nodeBuilder, release := c.getNodeBuilder()
	defer release()
	combinedFlags := toNodeBuilderFlags(flags) | nodebuilder.FlagsIgnoreErrors | nodebuilder.FlagsWriteTypeParametersInQualifiedName
	predicate := nodeBuilder.TypePredicateToTypePredicateNode(typePredicate, enclosingDeclaration, combinedFlags, nodebuilder.InternalFlagsNone, nil) // TODO: GH#18217
	printer_ := createPrinterWithRemoveComments(nodeBuilder.EmitContext())
	var sourceFile *ast.SourceFile
	if enclosingDeclaration != nil {
		sourceFile = ast.GetSourceFileOfNode(enclosingDeclaration)
	}
	printer_.Write(predicate /*sourceFile*/, sourceFile, writer, nil)
	return writer.String()
}

func (c *Checker) valueToString(value any) string {
	return ValueToString(value)
}

func (c *Checker) formatUnionTypes(types []*Type, expandingEnum bool) []*Type {
	var result []*Type
	var flags TypeFlags
	for i := 0; i < len(types); i++ {
		t := types[i]
		flags |= t.flags
		if t.flags&TypeFlagsNullable == 0 {
			if t.flags&TypeFlagsBooleanLiteral != 0 || (!expandingEnum && t.flags&TypeFlagsEnumLike != 0) {
				var baseType *Type
				if t.flags&TypeFlagsBooleanLiteral != 0 {
					baseType = c.booleanType
				} else {
					baseType = c.getBaseTypeOfEnumLikeType(t)
				}
				if baseType.flags&TypeFlagsUnion != 0 {
					count := len(baseType.AsUnionType().types)
					if i+count <= len(types) && c.getRegularTypeOfLiteralType(types[i+count-1]) == c.getRegularTypeOfLiteralType(baseType.AsUnionType().types[count-1]) {
						result = append(result, baseType)
						i += count - 1
						continue
					}
				}
			}
			result = append(result, t)
		}
	}
	if flags&TypeFlagsNull != 0 {
		result = append(result, c.nullType)
	}
	if flags&TypeFlagsUndefined != 0 {
		result = append(result, c.undefinedType)
	}
	return result
}

func (c *Checker) TypeToTypeNode(t *Type, enclosingDeclaration *ast.Node, flags nodebuilder.Flags, idToSymbol map[*ast.IdentifierNode]*ast.Symbol) *ast.TypeNode {
	nodeBuilder := c.getNodeBuilderEx(idToSymbol)
	return nodeBuilder.TypeToTypeNode(t, enclosingDeclaration, flags, nodebuilder.InternalFlagsNone, nil)
}

func (c *Checker) SignatureToSignatureDeclaration(signature *Signature, kind ast.Kind, enclosingDeclaration *ast.Node, flags nodebuilder.Flags) *ast.Node {
	nodeBuilder, release := c.getNodeBuilder()
	defer release()
	return nodeBuilder.SignatureToSignatureDeclaration(signature, kind, enclosingDeclaration, flags, nodebuilder.InternalFlagsNone, nil)
}

// ExpandSymbolForHover produces declaration strings for a symbol with verbosity support for expandable hover.
func (c *Checker) ExpandSymbolForHover(symbol *ast.Symbol, meaning ast.SymbolFlags, vc *VerbosityContext) string {
	nodeBuilder, release := c.getNodeBuilder()
	defer release()
	oldVerbosity := nodeBuilder.verbosity
	nodeBuilder.verbosity = vc
	defer func() {
		nodeBuilder.verbosity = oldVerbosity
	}()
	nodes := nodeBuilder.ExpandSymbolForHover(symbol, meaning)
	if len(nodes) == 0 {
		return ""
	}
	p := createPrinterWithRemoveComments(nodeBuilder.EmitContext())
	var sourceFile *ast.SourceFile
	if symbol.ValueDeclaration() != nil {
		sourceFile = ast.GetSourceFileOfNode(symbol.ValueDeclaration())
	}
	var b strings.Builder
	for i, node := range nodes {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Emit(node, sourceFile))
	}
	return b.String()
}

// TypeParameterToStringEx renders a type parameter declaration (e.g. "T extends Foo") with optional verbosity support.
func (c *Checker) TypeParameterToStringEx(t *Type, enclosingDeclaration *ast.Node, vc *VerbosityContext) string {
	nodeBuilder, release := c.getNodeBuilder()
	defer release()
	oldVerbosity := nodeBuilder.verbosity
	nodeBuilder.verbosity = vc
	defer func() {
		nodeBuilder.verbosity = oldVerbosity
	}()
	typeParamNode := nodeBuilder.TypeParameterToDeclaration(t, enclosingDeclaration, nodebuilder.FlagsIgnoreErrors, nodebuilder.InternalFlagsNone, nil)
	if typeParamNode == nil {
		return c.TypeToString(t)
	}
	p := createPrinterWithRemoveComments(nodeBuilder.EmitContext())
	var sourceFile *ast.SourceFile
	if enclosingDeclaration != nil {
		sourceFile = ast.GetSourceFileOfNode(enclosingDeclaration)
	}
	return p.Emit(typeParamNode, sourceFile)
}

func (c *Checker) TypeToTypeNodeEx(t *Type, enclosingDeclaration *ast.Node, flags nodebuilder.Flags, internalFlags nodebuilder.InternalFlags, idToSymbol map[*ast.IdentifierNode]*ast.Symbol) *ast.TypeNode {
	nodeBuilder := c.getNodeBuilderEx(idToSymbol)
	return nodeBuilder.TypeToTypeNode(t, enclosingDeclaration, flags, internalFlags, nil)
}

func (c *Checker) TypePredicateToTypePredicateNode(t *TypePredicate, enclosingDeclaration *ast.Node, flags nodebuilder.Flags, idToSymbol map[*ast.IdentifierNode]*ast.Symbol) *ast.TypePredicateNodeNode {
	nodeBuilder := c.getNodeBuilderEx(idToSymbol)
	return nodeBuilder.TypePredicateToTypePredicateNode(t, enclosingDeclaration, flags, nodebuilder.InternalFlagsNone, nil)
}
