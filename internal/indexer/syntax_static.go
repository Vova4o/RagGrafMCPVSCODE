package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/odvcencio/gotreesitter"
	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

func extractStaticSyntax(ctx context.Context, root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, language string) (syntaxFacts, error) {
	if err := ctx.Err(); err != nil {
		return syntaxFacts{}, err
	}
	if root == nil || lang == nil {
		return syntaxFacts{}, fmt.Errorf("extract %s syntax: missing syntax tree or language", language)
	}
	switch language {
	case "rust", "java", "kotlin", "c_sharp":
	default:
		return syntaxFacts{}, fmt.Errorf("extract static syntax: unsupported grammar %q", language)
	}

	var facts syntaxFacts
	err := walkSyntaxNamed(ctx, root, func(n *gotreesitter.Node) bool {
		typ := syntaxNodeType(n, lang)
		container := staticContainer(n, lang, src)
		if decl, ok := staticDeclaration(n, typ, container, lang, src, language); ok {
			facts.Declarations = append(facts.Declarations, decl)
		}
		if imp, ok := staticImport(n, typ, lang, src, language); ok {
			facts.Imports = append(facts.Imports, imp)
		}
		if call, ok := staticCall(n, typ, lang, src, language); ok {
			facts.Calls = append(facts.Calls, call)
		}
		facts.Bindings = append(facts.Bindings, staticBindings(n, typ, container, lang, src, language)...)
		facts.Heritage = append(facts.Heritage, staticHeritage(n, typ, lang, src, language)...)
		return true
	})
	if err != nil {
		return syntaxFacts{}, err
	}
	sort.SliceStable(facts.Declarations, func(i, j int) bool {
		if facts.Declarations[i].StartByte != facts.Declarations[j].StartByte {
			return facts.Declarations[i].StartByte < facts.Declarations[j].StartByte
		}
		return facts.Declarations[i].EndByte < facts.Declarations[j].EndByte
	})
	sort.SliceStable(facts.Imports, func(i, j int) bool {
		a, b := facts.Imports[i], facts.Imports[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Local != b.Local {
			return a.Local < b.Local
		}
		return a.Imported < b.Imported
	})
	sort.SliceStable(facts.Calls, func(i, j int) bool {
		if facts.Calls[i].StartByte != facts.Calls[j].StartByte {
			return facts.Calls[i].StartByte < facts.Calls[j].StartByte
		}
		return facts.Calls[i].Target < facts.Calls[j].Target
	})
	sort.SliceStable(facts.Bindings, func(i, j int) bool {
		if facts.Bindings[i].StartByte != facts.Bindings[j].StartByte {
			return facts.Bindings[i].StartByte < facts.Bindings[j].StartByte
		}
		return facts.Bindings[i].Field < facts.Bindings[j].Field
	})
	sort.SliceStable(facts.Heritage, func(i, j int) bool {
		if facts.Heritage[i].Type != facts.Heritage[j].Type {
			return facts.Heritage[i].Type < facts.Heritage[j].Type
		}
		return facts.Heritage[i].Super < facts.Heritage[j].Super
	})
	return facts, ctx.Err()
}

func staticDeclaration(n *gotreesitter.Node, typ, container string, lang *gotreesitter.Language, src []byte, language string) (syntaxDeclaration, bool) {
	var kind string
	var isInterface bool
	switch language {
	case "rust":
		switch typ {
		case "function_item":
			kind = graph.KindFunction
			if container != "" {
				kind = graph.KindMethod
			}
		case "function_signature_item":
			kind = graph.KindMethod
		case "struct_item", "enum_item", "type_item":
			kind = graph.KindType
		case "trait_item":
			kind = graph.KindType
			isInterface = true
		}
	case "java":
		switch typ {
		case "class_declaration", "enum_declaration", "record_declaration", "annotation_type_declaration":
			kind = graph.KindType
		case "interface_declaration":
			kind = graph.KindType
			isInterface = true
		case "method_declaration", "constructor_declaration":
			kind = graph.KindMethod
		}
	case "kotlin":
		switch typ {
		case "class_declaration":
			kind = graph.KindType
			isInterface = staticHasChildOfType(n, lang, "interface")
		case "object_declaration", "type_alias":
			kind = graph.KindType
		case "function_declaration":
			kind = graph.KindFunction
			if container != "" {
				kind = graph.KindMethod
			}
		}
	case "c_sharp":
		switch typ {
		case "class_declaration", "struct_declaration", "enum_declaration", "record_declaration", "record_struct_declaration":
			kind = graph.KindType
		case "interface_declaration":
			kind = graph.KindType
			isInterface = true
		case "method_declaration", "constructor_declaration":
			kind = graph.KindFunction
			if container != "" {
				kind = graph.KindMethod
			}
		case "local_function_statement":
			kind = graph.KindFunction
		}
	}
	if kind == "" {
		return syntaxDeclaration{}, false
	}
	nameNode := syntaxField(n, lang, "name")
	if nameNode == nil {
		nameNode = syntaxField(n, lang, "identifier")
	}
	if nameNode == nil && language == "kotlin" {
		nameNode = staticIdentifierChild(n, lang, "simple_identifier", "type_identifier")
	}
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return syntaxDeclaration{}, false
	}
	start, end := n.StartByte(), n.EndByte()
	if start > end || uint64(end) > uint64(len(src)) {
		return syntaxDeclaration{}, false
	}
	return syntaxDeclaration{
		Name: name, Kind: kind, Container: container,
		Detail: staticHeader(n, lang, src), StartByte: start, EndByte: end,
		Interface: isInterface,
	}, true
}

func staticImport(n *gotreesitter.Node, typ string, lang *gotreesitter.Language, src []byte, language string) (syntaxImport, bool) {
	wanted := false
	switch language {
	case "rust":
		wanted = typ == "use_declaration" || typ == "mod_item"
	case "java":
		wanted = typ == "import_declaration"
	case "kotlin":
		wanted = typ == "import_header"
	case "c_sharp":
		wanted = typ == "using_directive"
	}
	if !wanted {
		return syntaxImport{}, false
	}
	text := strings.TrimSpace(syntaxNodeText(n, src))
	pathNode := syntaxField(n, lang, "path")
	if pathNode == nil {
		pathNode = syntaxField(n, lang, "name")
	}
	path := strings.TrimSpace(syntaxNodeText(pathNode, src))
	if path == "" {
		path = text
	}
	path = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(path, ";"), ","))
	if strings.HasPrefix(path, "use ") {
		path = strings.TrimSpace(strings.TrimPrefix(path, "use "))
	}
	if strings.HasPrefix(path, "import ") {
		path = strings.TrimSpace(strings.TrimPrefix(path, "import "))
	}
	if strings.HasPrefix(path, "mod ") {
		path = strings.TrimSpace(strings.TrimPrefix(path, "mod "))
	}
	if strings.HasPrefix(path, "global using ") {
		path = strings.TrimSpace(strings.TrimPrefix(path, "global using "))
	} else if strings.HasPrefix(path, "using ") {
		path = strings.TrimSpace(strings.TrimPrefix(path, "using "))
	}
	path = strings.TrimSpace(strings.TrimPrefix(path, "static "))
	parts := strings.Split(path, " as ")
	if len(parts) == 1 && language == "c_sharp" {
		parts = strings.Split(path, " = ")
	}
	pathValue := strings.TrimSpace(parts[0])
	imported := staticLastSegment(pathValue)
	local := imported
	if len(parts) > 1 {
		if language == "c_sharp" && strings.Contains(text, " = ") {
			local = strings.TrimSpace(parts[0])
			pathValue = strings.TrimSpace(parts[len(parts)-1])
			imported = staticLastSegment(pathValue)
		} else {
			local = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	if imported == "" {
		return syntaxImport{}, false
	}
	return syntaxImport{Path: pathValue, Local: local, Imported: imported}, true
}

func staticCall(n *gotreesitter.Node, typ string, lang *gotreesitter.Language, src []byte, language string) (syntaxCall, bool) {
	constructorType := ""
	switch language {
	case "java", "c_sharp":
		if typ == "object_creation_expression" || typ == "implicit_object_creation_expression" {
			constructorType = syntaxNodeText(syntaxField(n, lang, "type"), src)
		}
	case "rust":
		if typ == "struct_expression" {
			constructorType = syntaxNodeText(syntaxField(n, lang, "name"), src)
			if constructorType == "" {
				constructorType = syntaxNodeText(syntaxField(n, lang, "type"), src)
			}
		}
	case "kotlin":
		if typ == "constructor_invocation" {
			constructorType = staticFirstNodeText(n, lang, src, "user_type")
		}
	}
	if constructorType != "" {
		constructorType = strings.TrimSpace(constructorType)
		return syntaxCall{Target: constructorType, StartByte: n.StartByte(), Constructor: true}, true
	}
	wanted := false
	switch language {
	case "rust", "kotlin":
		wanted = typ == "call_expression"
	case "java":
		wanted = typ == "method_invocation"
	case "c_sharp":
		wanted = typ == "invocation_expression"
	}
	if !wanted {
		return syntaxCall{}, false
	}
	var callee *gotreesitter.Node
	if language == "java" {
		object := syntaxField(n, lang, "object")
		name := syntaxField(n, lang, "name")
		if object != nil && name != nil {
			target := staticChainSegment(object, name, ".", lang, src)
			if target != "" {
				return syntaxCall{Target: target, StartByte: object.StartByte()}, true
			}
		}
	}
	for _, field := range []string{"function", "name", "expression", "callee"} {
		callee = syntaxField(n, lang, field)
		if callee != nil {
			break
		}
	}
	if callee == nil && language == "kotlin" {
		for i := 0; i < n.ChildCount(); i++ {
			child := n.Child(i)
			if isKotlinCallCallee(syntaxNodeType(child, lang)) {
				callee = child
				break
			}
		}
	}
	if callee == nil {
		return syntaxCall{}, false
	}
	target := staticCallCalleeText(callee, lang, src)
	if target == "" {
		return syntaxCall{}, false
	}
	return syntaxCall{Target: target, StartByte: callee.StartByte()}, true
}

func staticFirstNodeText(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, wanted string) string {
	if n == nil {
		return ""
	}
	if syntaxNodeType(n, lang) == wanted {
		return strings.TrimSpace(syntaxNodeText(n, src))
	}
	for i := 0; i < n.ChildCount(); i++ {
		if result := staticFirstNodeText(n.Child(i), lang, src, wanted); result != "" {
			return result
		}
	}
	return ""
}

func isKotlinCallCallee(typ string) bool {
	switch typ {
	case "simple_identifier", "navigation_expression", "this_expression", "super_expression", "call_expression":
		return true
	default:
		return false
	}
}

func staticContainer(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	for parent := n.Parent(); parent != nil; parent = parent.Parent() {
		typ := syntaxNodeType(parent, lang)
		if !isStaticContainer(typ) {
			continue
		}
		name := syntaxField(parent, lang, "name")
		if name == nil {
			name = syntaxField(parent, lang, "type")
		}
		name = staticUnwrapGenericType(name, lang)
		if name == nil {
			switch typ {
			case "class_declaration", "object_declaration", "companion_object":
				name = staticIdentifierChild(parent, lang, "type_identifier", "simple_identifier")
			}
		}
		text := strings.TrimSpace(syntaxNodeText(name, src))
		if text == "" && typ == "companion_object" {
			text = "Companion"
		}
		return text
	}
	return ""
}

func staticIdentifierChild(n *gotreesitter.Node, lang *gotreesitter.Language, kinds ...string) *gotreesitter.Node {
	for i := 0; i < n.ChildCount(); i++ {
		child := n.Child(i)
		for _, kind := range kinds {
			if syntaxNodeType(child, lang) == kind {
				return child
			}
		}
	}
	return nil
}

func isStaticContainer(typ string) bool {
	switch typ {
	case "impl_item", "trait_item", "struct_item", "enum_item", "class_declaration", "interface_declaration", "enum_declaration", "record_declaration", "annotation_type_declaration", "object_declaration", "companion_object", "struct_declaration", "record_struct_declaration":
		return true
	default:
		return false
	}
}

func staticHeader(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	start, end := int(n.StartByte()), int(n.EndByte())
	if start < 0 || end < start || end > len(src) {
		return ""
	}
	body := syntaxField(n, lang, "body")
	if body == nil {
		body = syntaxField(n, lang, "declaration_list")
	}
	if body != nil && int(body.StartByte()) >= start && int(body.StartByte()) <= end {
		end = int(body.StartByte())
	} else if i := strings.IndexByte(string(src[start:end]), '{'); i >= 0 {
		end = start + i
	}
	header := strings.Join(strings.Fields(string(src[start:end])), " ")
	if len(header) > 240 {
		header = header[:240]
	}
	return header
}

// staticUnwrapGenericType strips a Rust `impl<T> Foo<T>` style "type" field
// down to the plain type name node ("Foo"), so containers and heritage share
// the same simple name as struct/trait declarations, which never carry
// generics in their own "name" field.
func staticUnwrapGenericType(node *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	if node != nil && syntaxNodeType(node, lang) == "generic_type" {
		if inner := syntaxField(node, lang, "type"); inner != nil {
			return inner
		}
	}
	return node
}

// staticStripGenericArgs removes a trailing "<...>" type-argument list from a
// type name written as source text (e.g. "List<T>" -> "List"), keeping any
// qualifiers before it (e.g. "a.b.C<T>" -> "a.b.C").
func staticStripGenericArgs(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '<'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// staticUnresolvedReceiverMarker replaces the qualifier of a call target
// whose receiver expression is not a plain identifier/this/field chain (for
// example it starts from a call or index result, as in "foo().bar()"). The
// resulting target ("<expr>.bar") cannot be mistaken for a real dotted name
// by the resolver, which is the point: silently keeping the flattened or
// partial text risks binding the call to an unrelated declaration.
const staticUnresolvedReceiverMarker = "<expr>"

// staticNameChainText renders a receiver expression as a dotted name
// ("this.indexer", "a.b") or, for Rust paths, a "::" scoped name
// ("crate::a::Foo"), but only when it is built entirely from
// identifiers/this/self/super and member-access nodes. ok is false as soon
// as the chain contains a call, index, or any other non-name expression;
// callers must then use staticUnresolvedReceiverMarker instead of this
// (possibly partial and misleading) text.
func staticNameChainText(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) (string, bool) {
	if n == nil {
		return "", true
	}
	switch syntaxNodeType(n, lang) {
	case "identifier", "simple_identifier", "type_identifier", "field_identifier",
		"this", "this_expression", "self", "super", "super_expression":
		return strings.TrimSpace(syntaxNodeText(n, src)), true
	case "field_access":
		left, ok := staticNameChainText(syntaxField(n, lang, "object"), lang, src)
		if !ok {
			return "", false
		}
		return staticJoinChain(left, syntaxNodeText(syntaxField(n, lang, "field"), src), "."), true
	case "member_access_expression":
		left, ok := staticNameChainText(syntaxField(n, lang, "expression"), lang, src)
		if !ok {
			return "", false
		}
		return staticJoinChain(left, syntaxNodeText(syntaxField(n, lang, "name"), src), "."), true
	case "field_expression":
		left, ok := staticNameChainText(syntaxField(n, lang, "value"), lang, src)
		if !ok {
			return "", false
		}
		return staticJoinChain(left, syntaxNodeText(syntaxField(n, lang, "field"), src), "."), true
	case "scoped_identifier", "scoped_type_identifier":
		left, ok := staticNameChainText(syntaxField(n, lang, "path"), lang, src)
		if !ok {
			return "", false
		}
		return staticJoinChain(left, syntaxNodeText(syntaxField(n, lang, "name"), src), "::"), true
	case "navigation_expression":
		if n.ChildCount() < 2 {
			return "", false
		}
		suffix := n.Child(n.ChildCount() - 1)
		if syntaxNodeType(suffix, lang) != "navigation_suffix" {
			return "", false
		}
		left, ok := staticNameChainText(n.Child(0), lang, src)
		if !ok {
			return "", false
		}
		nameNode := staticIdentifierChild(suffix, lang, "simple_identifier")
		if nameNode == nil {
			return "", false
		}
		return staticJoinChain(left, syntaxNodeText(nameNode, src), "."), true
	default:
		return "", false
	}
}

func staticJoinChain(left, right, sep string) string {
	right = strings.TrimSpace(right)
	if left == "" {
		return right
	}
	return left + sep + right
}

// staticCallCalleeText renders the target of a call from its callee node. For
// member access / field access / navigation / scoped-path nodes it splits
// off the final segment and resolves the remaining receiver through
// staticNameChainText, substituting staticUnresolvedReceiverMarker when that
// receiver is not a plain name chain. For everything else it falls back to
// staticNameChainText directly (a bare identifier, this, and similar).
func staticCallCalleeText(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil {
		return ""
	}
	switch syntaxNodeType(n, lang) {
	case "field_access":
		return staticChainSegment(syntaxField(n, lang, "object"), syntaxField(n, lang, "field"), ".", lang, src)
	case "member_access_expression":
		return staticChainSegment(syntaxField(n, lang, "expression"), syntaxField(n, lang, "name"), ".", lang, src)
	case "field_expression":
		return staticChainSegment(syntaxField(n, lang, "value"), syntaxField(n, lang, "field"), ".", lang, src)
	case "scoped_identifier", "scoped_type_identifier":
		return staticChainSegment(syntaxField(n, lang, "path"), syntaxField(n, lang, "name"), "::", lang, src)
	case "navigation_expression":
		if n.ChildCount() < 2 {
			return ""
		}
		suffix := n.Child(n.ChildCount() - 1)
		if syntaxNodeType(suffix, lang) != "navigation_suffix" {
			return ""
		}
		nameNode := staticIdentifierChild(suffix, lang, "simple_identifier")
		return staticChainSegment(n.Child(0), nameNode, ".", lang, src)
	default:
		text, ok := staticNameChainText(n, lang, src)
		if ok {
			return text
		}
		return staticUnresolvedReceiverMarker
	}
}

// staticChainSegment combines a receiver node and a final name node into a
// call target, using staticUnresolvedReceiverMarker for the qualifier when
// the receiver is not a plain name chain (see staticNameChainText).
func staticChainSegment(receiver, name *gotreesitter.Node, sep string, lang *gotreesitter.Language, src []byte) string {
	nameText := strings.TrimSpace(syntaxNodeText(name, src))
	if nameText == "" {
		return ""
	}
	qualifier, ok := staticNameChainText(receiver, lang, src)
	if !ok {
		qualifier = staticUnresolvedReceiverMarker
	}
	if qualifier == "" {
		return nameText
	}
	return qualifier + sep + nameText
}

func staticLastSegment(s string) string {
	if i := strings.LastIndexAny(s, ".:"); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

// staticChildOfType returns the first direct child of n whose grammar type
// is typ. It is used for grammars (mainly Kotlin) that leave most of a
// node's children unnamed, so ChildByFieldName cannot find them.
func staticChildOfType(n *gotreesitter.Node, lang *gotreesitter.Language, typ string) *gotreesitter.Node {
	if n == nil {
		return nil
	}
	for i := 0; i < n.ChildCount(); i++ {
		if syntaxNodeType(n.Child(i), lang) == typ {
			return n.Child(i)
		}
	}
	return nil
}

func staticHasChildOfType(n *gotreesitter.Node, lang *gotreesitter.Language, typ string) bool {
	return staticChildOfType(n, lang, typ) != nil
}

// staticAncestorOfType returns the nearest strict ancestor of n whose
// grammar type is typ.
func staticAncestorOfType(n *gotreesitter.Node, lang *gotreesitter.Language, typ string) *gotreesitter.Node {
	for parent := n.Parent(); parent != nil; parent = parent.Parent() {
		if syntaxNodeType(parent, lang) == typ {
			return parent
		}
	}
	return nil
}

// staticEnclosingFunctionLike returns the nearest strict ancestor that owns
// a parameter list: a method/constructor/function declaration or a
// lambda/closure expression. Parameters are always siblings of their body
// rather than its ancestor, so callers use this together with
// staticScopeBounds instead of an ancestor block search.
func staticEnclosingFunctionLike(n *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	for parent := n.Parent(); parent != nil; parent = parent.Parent() {
		switch syntaxNodeType(parent, lang) {
		case "method_declaration", "constructor_declaration", "local_function_statement",
			"function_item", "function_declaration", "lambda_expression", "lambda_literal",
			"closure_expression":
			return parent
		}
	}
	return nil
}

// staticScopeNode returns the scope-owning node whose end byte bounds a
// binding rooted at owner: the named "body" field when the grammar labels
// it (Java/C#/Rust and loop/catch nodes that own their own body), a
// body-shaped child found by type for grammars that leave fields unnamed
// (Kotlin function bodies), or owner itself for single-expression bodies,
// bodyless interface methods, and Kotlin lambda literals (whose body is not
// a separate node). Returns nil only when owner is nil.
func staticScopeNode(owner *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	if owner == nil {
		return nil
	}
	if syntaxNodeType(owner, lang) == "lambda_literal" {
		return owner
	}
	if body := syntaxField(owner, lang, "body"); body != nil {
		return body
	}
	if body := staticChildOfType(owner, lang, "function_body"); body != nil {
		return body
	}
	return owner
}

// staticScopeBounds returns the start and end byte of staticScopeNode(owner,
// lang), or (0, 0) when owner is nil.
func staticScopeBounds(owner *gotreesitter.Node, lang *gotreesitter.Language) (start, end uint32) {
	node := staticScopeNode(owner, lang)
	if node == nil {
		return 0, 0
	}
	return node.StartByte(), node.EndByte()
}

// staticEnclosingBlockNode approximates a name's lexical scope by finding
// the nearest enclosing block-shaped ancestor. This only works for bindings
// whose scope really is an ancestor of the declaring node (locals,
// for-loop/catch bodies that own their body directly); function/lambda
// parameters must use staticEnclosingFunctionLike + staticScopeBounds
// instead, since their body is a sibling, not an ancestor. Returns nil when
// no such ancestor exists.
func staticEnclosingBlockNode(n *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	for parent := n.Parent(); parent != nil; parent = parent.Parent() {
		if staticIsScopeBlock(syntaxNodeType(parent, lang)) {
			return parent
		}
	}
	return nil
}

// staticEnclosingBlockBounds returns the start and end byte of
// staticEnclosingBlockNode(n, lang), falling back to n's own bounds when no
// enclosing block is found.
func staticEnclosingBlockBounds(n *gotreesitter.Node, lang *gotreesitter.Language) (start, end uint32) {
	block := staticEnclosingBlockNode(n, lang)
	if block == nil {
		return n.StartByte(), n.EndByte()
	}
	return block.StartByte(), block.EndByte()
}

func staticIsScopeBlock(typ string) bool {
	switch typ {
	case "block", "constructor_body", "function_body", "lambda_literal", "control_structure_body", "catch_block":
		return true
	default:
		return false
	}
}

func staticFieldBinding(container, field, typ string, start, end uint32) syntaxBinding {
	return syntaxBinding{Container: container, Field: field, Type: typ, Local: false, StartByte: start, EndByte: end}
}

func staticLocalBinding(container, field, typ string, start, end, scopeStart uint32) syntaxBinding {
	return syntaxBinding{Container: container, Field: field, Type: typ, Local: true, StartByte: start, EndByte: end, ScopeStart: scopeStart}
}

// staticCreationExpressionType returns the constructed type of a
// "new Foo(...)" value node (Java object_creation_expression / C#
// object_creation_expression), or "" for any other initializer (method
// calls, literals, other expressions): only an explicit constructor call is
// confidently typed here.
func staticCreationExpressionType(value *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if value == nil {
		return ""
	}
	switch syntaxNodeType(value, lang) {
	case "object_creation_expression", "implicit_object_creation_expression":
		return strings.TrimSpace(syntaxNodeText(syntaxField(value, lang, "type"), src))
	}
	return ""
}

func staticBindings(n *gotreesitter.Node, typ, container string, lang *gotreesitter.Language, src []byte, language string) []syntaxBinding {
	switch language {
	case "java":
		return staticJavaBindings(n, typ, container, lang, src)
	case "c_sharp":
		return staticCSharpBindings(n, typ, container, lang, src)
	case "kotlin":
		return staticKotlinBindings(n, typ, container, lang, src)
	case "rust":
		return staticRustBindings(n, typ, container, lang, src)
	}
	return nil
}

func staticHeritage(n *gotreesitter.Node, typ string, lang *gotreesitter.Language, src []byte, language string) []syntaxHeritage {
	switch language {
	case "java":
		return staticJavaHeritage(n, typ, lang, src)
	case "c_sharp":
		return staticCSharpHeritage(n, typ, lang, src)
	case "kotlin":
		return staticKotlinHeritage(n, typ, lang, src)
	case "rust":
		return staticRustHeritage(n, typ, lang, src)
	}
	return nil
}

func staticDeclarationSimpleName(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	nameNode := syntaxField(n, lang, "name")
	if nameNode == nil {
		nameNode = staticIdentifierChild(n, lang, "type_identifier", "simple_identifier")
	}
	return strings.TrimSpace(syntaxNodeText(nameNode, src))
}

// ---- Java ----

func staticJavaBindings(n *gotreesitter.Node, typ, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	switch typ {
	case "field_declaration":
		return staticJavaFieldBindings(n, container, lang, src)
	case "formal_parameter":
		return staticParameterBinding(n, container, lang, src)
	case "catch_formal_parameter":
		return staticJavaCatchBinding(n, container, lang, src)
	case "local_variable_declaration":
		return staticJavaLocalBindings(n, container, lang, src)
	case "enhanced_for_statement":
		return staticJavaForEachBinding(n, container, lang, src)
	case "lambda_expression":
		return staticJavaLambdaParamBindings(n, container, lang, src)
	case "assignment_expression":
		return staticFieldAssignmentBinding(n, container, "field_access", "object", "field", lang, src)
	}
	return nil
}

func staticJavaFieldBindings(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	if container == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
	var bindings []syntaxBinding
	for i := 0; i < n.ChildCount(); i++ {
		if n.FieldNameForChild(i, lang) != "declarator" {
			continue
		}
		declarator := n.Child(i)
		nameNode := syntaxField(declarator, lang, "name")
		name := strings.TrimSpace(syntaxNodeText(nameNode, src))
		if name == "" {
			continue
		}
		bindings = append(bindings, staticFieldBinding(container, name, typeText, n.StartByte(), n.EndByte()))
	}
	return bindings
}

func staticParameterBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	nameNode := syntaxField(n, lang, "name")
	if nameNode == nil {
		return nil
	}
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
	scopeStart, end := staticScopeBounds(staticEnclosingFunctionLike(n, lang), lang)
	if end == 0 {
		end = n.EndByte()
		scopeStart = n.StartByte()
	}
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart)}
}

func staticJavaCatchBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	nameNode := syntaxField(n, lang, "name")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(staticChildOfType(n, lang, "catch_type"), src))
	scopeStart, end := staticScopeBounds(staticAncestorOfType(n, lang, "catch_clause"), lang)
	if end == 0 {
		end = n.EndByte()
		scopeStart = n.StartByte()
	}
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart)}
}

func staticJavaLocalBindings(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	typeNode := syntaxField(n, lang, "type")
	typeText := strings.TrimSpace(syntaxNodeText(typeNode, src))
	isVar := typeText == "var"
	scopeStart, end := staticEnclosingBlockBounds(n, lang)
	var bindings []syntaxBinding
	for i := 0; i < n.ChildCount(); i++ {
		if n.FieldNameForChild(i, lang) != "declarator" {
			continue
		}
		declarator := n.Child(i)
		nameNode := syntaxField(declarator, lang, "name")
		name := strings.TrimSpace(syntaxNodeText(nameNode, src))
		if name == "" {
			continue
		}
		declType := typeText
		if isVar {
			declType = staticCreationExpressionType(syntaxField(declarator, lang, "value"), lang, src)
		}
		bindings = append(bindings, staticLocalBinding(container, name, declType, nameNode.StartByte(), end, scopeStart))
	}
	return bindings
}

func staticJavaForEachBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	nameNode := syntaxField(n, lang, "name")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
	scopeStart, end := staticScopeBounds(n, lang)
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart)}
}

func staticJavaLambdaParamBindings(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	params := syntaxField(n, lang, "parameters")
	if params == nil {
		return nil
	}
	scopeStart, end := staticScopeBounds(n, lang)
	var bindings []syntaxBinding
	switch syntaxNodeType(params, lang) {
	case "identifier":
		name := strings.TrimSpace(syntaxNodeText(params, src))
		if name != "" {
			bindings = append(bindings, staticLocalBinding(container, name, "", params.StartByte(), end, scopeStart))
		}
	case "formal_parameters", "inferred_parameters":
		for i := 0; i < params.ChildCount(); i++ {
			child := params.Child(i)
			switch syntaxNodeType(child, lang) {
			case "formal_parameter":
				nameNode := syntaxField(child, lang, "name")
				name := strings.TrimSpace(syntaxNodeText(nameNode, src))
				if name == "" {
					continue
				}
				typeText := strings.TrimSpace(syntaxNodeText(syntaxField(child, lang, "type"), src))
				bindings = append(bindings, staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart))
			case "identifier":
				name := strings.TrimSpace(syntaxNodeText(child, src))
				if name == "" {
					continue
				}
				bindings = append(bindings, staticLocalBinding(container, name, "", child.StartByte(), end, scopeStart))
			}
		}
	}
	return bindings
}

// staticFieldAssignmentBinding recognises "this.x = new Foo();" style
// constructor assignments (Java field_access, C# member_access_expression)
// and records the field with the constructed type. Assignments whose value
// is not a "new T(...)" expression are skipped: without that, the type
// would only be a guess.
func staticFieldAssignmentBinding(n *gotreesitter.Node, container, leftType, receiverField, nameField string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	if container == "" {
		return nil
	}
	left := syntaxField(n, lang, "left")
	right := syntaxField(n, lang, "right")
	if left == nil || right == nil || syntaxNodeType(left, lang) != leftType {
		return nil
	}
	receiver := syntaxField(left, lang, receiverField)
	receiverTyp := syntaxNodeType(receiver, lang)
	if receiverTyp != "this" && receiverTyp != "this_expression" {
		return nil
	}
	nameNode := syntaxField(left, lang, nameField)
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := staticCreationExpressionType(right, lang, src)
	if typeText == "" {
		return nil
	}
	return []syntaxBinding{staticFieldBinding(container, name, typeText, n.StartByte(), n.EndByte())}
}

func staticJavaHeritage(n *gotreesitter.Node, typ string, lang *gotreesitter.Language, src []byte) []syntaxHeritage {
	switch typ {
	case "class_declaration":
		name := staticDeclarationSimpleName(n, lang, src)
		if name == "" {
			return nil
		}
		var heritage []syntaxHeritage
		if superclass := syntaxField(n, lang, "superclass"); superclass != nil {
			if text := staticJavaSingleTypeText(superclass, lang, src); text != "" {
				heritage = append(heritage, syntaxHeritage{Type: name, Super: text, Kind: syntaxHeritageExtends})
			}
		}
		if interfaces := syntaxField(n, lang, "interfaces"); interfaces != nil {
			heritage = append(heritage, staticJavaTypeListHeritage(name, interfaces, syntaxHeritageImplements, lang, src)...)
		}
		return heritage
	case "interface_declaration":
		name := staticDeclarationSimpleName(n, lang, src)
		if name == "" {
			return nil
		}
		if extendsList := staticChildOfType(n, lang, "extends_interfaces"); extendsList != nil {
			return staticJavaTypeListHeritage(name, extendsList, syntaxHeritageExtends, lang, src)
		}
	}
	return nil
}

func staticJavaSingleTypeText(wrapper *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	for i := 0; i < wrapper.ChildCount(); i++ {
		child := wrapper.Child(i)
		switch syntaxNodeType(child, lang) {
		case "type_identifier", "generic_type", "scoped_type_identifier":
			return staticStripGenericArgs(syntaxNodeText(child, src))
		}
	}
	return ""
}

func staticJavaTypeListHeritage(typeName string, wrapper *gotreesitter.Node, kind string, lang *gotreesitter.Language, src []byte) []syntaxHeritage {
	list := staticChildOfType(wrapper, lang, "type_list")
	if list == nil {
		return nil
	}
	var heritage []syntaxHeritage
	for i := 0; i < list.ChildCount(); i++ {
		child := list.Child(i)
		switch syntaxNodeType(child, lang) {
		case "type_identifier", "generic_type", "scoped_type_identifier":
			text := staticStripGenericArgs(syntaxNodeText(child, src))
			if text != "" {
				heritage = append(heritage, syntaxHeritage{Type: typeName, Super: text, Kind: kind})
			}
		}
	}
	return heritage
}

// ---- C# ----

func staticCSharpBindings(n *gotreesitter.Node, typ, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	switch typ {
	case "field_declaration":
		return staticCSharpFieldDeclarationBindings(n, container, lang, src)
	case "property_declaration":
		return staticCSharpPropertyBinding(n, container, lang, src)
	case "parameter":
		return staticParameterBinding(n, container, lang, src)
	case "catch_declaration":
		return staticCSharpCatchBinding(n, container, lang, src)
	case "foreach_statement":
		return staticCSharpForEachBinding(n, container, lang, src)
	case "lambda_expression":
		return staticCSharpLambdaParamBindings(n, container, lang, src)
	case "local_declaration_statement":
		return staticCSharpLocalBindings(n, container, lang, src)
	case "declaration_expression", "declaration_pattern":
		return staticCSharpPatternBinding(n, container, lang, src)
	case "assignment_expression":
		return staticFieldAssignmentBinding(n, container, "member_access_expression", "expression", "name", lang, src)
	}
	return nil
}

func staticCSharpFieldDeclarationBindings(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	if container == "" {
		return nil
	}
	varDecl := staticChildOfType(n, lang, "variable_declaration")
	if varDecl == nil {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(varDecl, lang, "type"), src))
	var bindings []syntaxBinding
	for i := 0; i < varDecl.ChildCount(); i++ {
		if syntaxNodeType(varDecl.Child(i), lang) != "variable_declarator" {
			continue
		}
		declarator := varDecl.Child(i)
		nameNode := syntaxField(declarator, lang, "name")
		name := strings.TrimSpace(syntaxNodeText(nameNode, src))
		if name == "" {
			continue
		}
		bindings = append(bindings, staticFieldBinding(container, name, typeText, n.StartByte(), n.EndByte()))
	}
	return bindings
}

func staticCSharpPropertyBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	if container == "" {
		return nil
	}
	nameNode := syntaxField(n, lang, "name")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
	return []syntaxBinding{staticFieldBinding(container, name, typeText, n.StartByte(), n.EndByte())}
}

func staticCSharpCatchBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	nameNode := syntaxField(n, lang, "name")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
	scopeStart, end := staticScopeBounds(staticAncestorOfType(n, lang, "catch_clause"), lang)
	if end == 0 {
		end = n.EndByte()
		scopeStart = n.StartByte()
	}
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart)}
}

func staticCSharpForEachBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	nameNode := syntaxField(n, lang, "left")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
	scopeStart, end := staticScopeBounds(n, lang)
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart)}
}

func staticCSharpLambdaParamBindings(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	params := syntaxField(n, lang, "parameters")
	if params == nil {
		return nil
	}
	scopeStart, end := staticScopeBounds(n, lang)
	var bindings []syntaxBinding
	switch syntaxNodeType(params, lang) {
	case "implicit_parameter", "identifier":
		name := strings.TrimSpace(syntaxNodeText(params, src))
		if name != "" {
			bindings = append(bindings, staticLocalBinding(container, name, "", params.StartByte(), end, scopeStart))
		}
	case "parameter_list":
		for i := 0; i < params.ChildCount(); i++ {
			child := params.Child(i)
			switch syntaxNodeType(child, lang) {
			case "parameter":
				nameNode := syntaxField(child, lang, "name")
				name := strings.TrimSpace(syntaxNodeText(nameNode, src))
				if name == "" {
					continue
				}
				typeText := strings.TrimSpace(syntaxNodeText(syntaxField(child, lang, "type"), src))
				bindings = append(bindings, staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart))
			case "identifier":
				name := strings.TrimSpace(syntaxNodeText(child, src))
				if name == "" {
					continue
				}
				bindings = append(bindings, staticLocalBinding(container, name, "", child.StartByte(), end, scopeStart))
			}
		}
	}
	return bindings
}

func staticCSharpLocalBindings(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	varDecl := staticChildOfType(n, lang, "variable_declaration")
	if varDecl == nil {
		return nil
	}
	typeNode := syntaxField(varDecl, lang, "type")
	typeText := strings.TrimSpace(syntaxNodeText(typeNode, src))
	isVar := typeText == "var" || syntaxNodeType(typeNode, lang) == "implicit_type"
	scopeStart, end := staticEnclosingBlockBounds(n, lang)
	var bindings []syntaxBinding
	for i := 0; i < varDecl.ChildCount(); i++ {
		if syntaxNodeType(varDecl.Child(i), lang) != "variable_declarator" {
			continue
		}
		declarator := varDecl.Child(i)
		nameNode := syntaxField(declarator, lang, "name")
		name := strings.TrimSpace(syntaxNodeText(nameNode, src))
		if name == "" {
			continue
		}
		declType := typeText
		if isVar {
			declType = staticCreationExpressionType(syntaxField(declarator, lang, "value"), lang, src)
		}
		bindings = append(bindings, staticLocalBinding(container, name, declType, nameNode.StartByte(), end, scopeStart))
	}
	return bindings
}

// staticCSharpPatternBinding covers "out var x"/"out Foo y" declaration
// expressions and "is Foo f" declaration patterns. These are always typed
// "" per the binding contract: unlike a local declaration, the written type
// here is a runtime type check, not a guaranteed static type of the name
// everywhere it is subsequently used.
func staticCSharpPatternBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	nameNode := syntaxField(n, lang, "name")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	scopeStart, end := staticEnclosingBlockBounds(n, lang)
	return []syntaxBinding{staticLocalBinding(container, name, "", nameNode.StartByte(), end, scopeStart)}
}

func staticCSharpHeritage(n *gotreesitter.Node, typ string, lang *gotreesitter.Language, src []byte) []syntaxHeritage {
	switch typ {
	case "class_declaration", "struct_declaration", "interface_declaration", "record_declaration", "record_struct_declaration":
	default:
		return nil
	}
	name := staticDeclarationSimpleName(n, lang, src)
	if name == "" {
		return nil
	}
	baseList := staticChildOfType(n, lang, "base_list")
	if baseList == nil {
		return nil
	}
	var heritage []syntaxHeritage
	for i := 0; i < baseList.ChildCount(); i++ {
		child := baseList.Child(i)
		switch syntaxNodeType(child, lang) {
		case "identifier", "generic_name", "qualified_name":
			text := staticStripGenericArgs(syntaxNodeText(child, src))
			if text != "" {
				heritage = append(heritage, syntaxHeritage{Type: name, Super: text, Kind: ""})
			}
		}
	}
	return heritage
}

// ---- Kotlin ----

func staticKotlinBindings(n *gotreesitter.Node, typ, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	switch typ {
	case "class_parameter":
		return staticKotlinClassParameterBinding(n, container, lang, src)
	case "property_declaration":
		return staticKotlinPropertyBinding(n, container, lang, src)
	case "parameter":
		return staticKotlinParameterBinding(n, container, lang, src)
	case "for_statement":
		return staticKotlinForBinding(n, container, lang, src)
	case "catch_block":
		return staticKotlinCatchBinding(n, container, lang, src)
	case "lambda_literal":
		return staticKotlinLambdaBindings(n, container, lang, src)
	}
	return nil
}

func staticKotlinClassParameterBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	if container == "" || !staticHasChildOfType(n, lang, "binding_pattern_kind") {
		return nil
	}
	nameNode := staticIdentifierChild(n, lang, "simple_identifier")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(staticChildOfType(n, lang, "user_type"), src))
	return []syntaxBinding{staticFieldBinding(container, name, typeText, n.StartByte(), n.EndByte())}
}

func staticKotlinPropertyBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	parentType := ""
	if parent := n.Parent(); parent != nil {
		parentType = syntaxNodeType(parent, lang)
	}
	if parentType != "class_body" {
		return staticKotlinLocalPropertyBinding(n, parentType, container, lang, src)
	}
	if container == "" {
		return nil
	}
	varDecl := staticChildOfType(n, lang, "variable_declaration")
	if varDecl == nil {
		return nil
	}
	nameNode := staticIdentifierChild(varDecl, lang, "simple_identifier")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(staticChildOfType(varDecl, lang, "user_type"), src))
	return []syntaxBinding{staticFieldBinding(container, name, typeText, n.StartByte(), n.EndByte())}
}

// staticKotlinLocalPropertyBinding handles "val"/"var" declarations that are
// not class members, i.e. locals inside a function or lambda body
// (property_declaration is reused by the grammar for both).
func staticKotlinLocalPropertyBinding(n *gotreesitter.Node, parentType, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	if parentType != "statements" {
		return nil
	}
	varDecl := staticChildOfType(n, lang, "variable_declaration")
	if varDecl == nil {
		return nil
	}
	nameNode := staticIdentifierChild(varDecl, lang, "simple_identifier")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(staticChildOfType(varDecl, lang, "user_type"), src))
	if typeText == "" {
		typeText = staticKotlinInitializerType(n, lang, src)
	}
	scopeStart, end := staticEnclosingBlockBounds(n, lang)
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart)}
}

// staticKotlinInitializerType infers a local's type from "val x = Foo()"
// only when the initializer is a call whose callee is a capitalised simple
// name; builder-style calls like "Foo.create()" or lower-case factory
// functions are left untyped rather than guessed.
func staticKotlinInitializerType(propertyDecl *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if propertyDecl.ChildCount() == 0 {
		return ""
	}
	value := propertyDecl.Child(propertyDecl.ChildCount() - 1)
	if syntaxNodeType(value, lang) != "call_expression" {
		return ""
	}
	callee := value.Child(0)
	if syntaxNodeType(callee, lang) != "simple_identifier" {
		return ""
	}
	name := strings.TrimSpace(syntaxNodeText(callee, src))
	r := []rune(name)
	if len(r) == 0 || !unicode.IsUpper(r[0]) {
		return ""
	}
	return name
}

func staticKotlinParameterBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	nameNode := staticIdentifierChild(n, lang, "simple_identifier")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(staticChildOfType(n, lang, "user_type"), src))
	scopeStart, end := staticScopeBounds(staticEnclosingFunctionLike(n, lang), lang)
	if end == 0 {
		end = n.EndByte()
		scopeStart = n.StartByte()
	}
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart)}
}

func staticKotlinForBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	varDecl := staticChildOfType(n, lang, "variable_declaration")
	if varDecl == nil {
		return nil
	}
	nameNode := staticIdentifierChild(varDecl, lang, "simple_identifier")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(staticChildOfType(varDecl, lang, "user_type"), src))
	body := staticChildOfType(n, lang, "control_structure_body")
	end := n.EndByte()
	scopeStart := n.StartByte()
	if body != nil {
		end = body.EndByte()
		scopeStart = body.StartByte()
	}
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart)}
}

func staticKotlinCatchBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	nameNode := staticIdentifierChild(n, lang, "simple_identifier")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(staticChildOfType(n, lang, "user_type"), src))
	return []syntaxBinding{staticLocalBinding(container, name, typeText, nameNode.StartByte(), n.EndByte(), n.StartByte())}
}

// staticKotlinLambdaBindings handles both explicit lambda parameters
// ("{ x -> }", "{ x: Foo -> }") and the implicit "it" parameter that Kotlin
// gives every parameterless lambda literal.
func staticKotlinLambdaBindings(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	end := n.EndByte()
	scopeStart := n.StartByte()
	params := staticChildOfType(n, lang, "lambda_parameters")
	if params == nil {
		return []syntaxBinding{staticLocalBinding(container, "it", "", n.StartByte(), end, scopeStart)}
	}
	var bindings []syntaxBinding
	for i := 0; i < params.ChildCount(); i++ {
		child := params.Child(i)
		switch syntaxNodeType(child, lang) {
		case "variable_declaration":
			nameNode := staticIdentifierChild(child, lang, "simple_identifier")
			name := strings.TrimSpace(syntaxNodeText(nameNode, src))
			if name == "" {
				continue
			}
			typeText := strings.TrimSpace(syntaxNodeText(staticChildOfType(child, lang, "user_type"), src))
			bindings = append(bindings, staticLocalBinding(container, name, typeText, nameNode.StartByte(), end, scopeStart))
		case "simple_identifier":
			name := strings.TrimSpace(syntaxNodeText(child, src))
			if name == "" {
				continue
			}
			bindings = append(bindings, staticLocalBinding(container, name, "", child.StartByte(), end, scopeStart))
		}
	}
	return bindings
}

func staticKotlinHeritage(n *gotreesitter.Node, typ string, lang *gotreesitter.Language, src []byte) []syntaxHeritage {
	if typ != "class_declaration" {
		return nil
	}
	name := staticDeclarationSimpleName(n, lang, src)
	if name == "" {
		return nil
	}
	var heritage []syntaxHeritage
	for i := 0; i < n.ChildCount(); i++ {
		if syntaxNodeType(n.Child(i), lang) != "delegation_specifier" {
			continue
		}
		spec := n.Child(i)
		userType := staticChildOfType(spec, lang, "user_type")
		if userType == nil {
			if inv := staticChildOfType(spec, lang, "constructor_invocation"); inv != nil {
				userType = staticChildOfType(inv, lang, "user_type")
			}
		}
		text := staticStripGenericArgs(syntaxNodeText(userType, src))
		if text != "" {
			heritage = append(heritage, syntaxHeritage{Type: name, Super: text, Kind: ""})
		}
	}
	return heritage
}

// ---- Rust ----

func staticRustBindings(n *gotreesitter.Node, typ, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	switch typ {
	case "field_declaration":
		return staticRustStructFieldBinding(n, container, lang, src)
	case "parameter":
		return staticRustParameterBinding(n, container, lang, src)
	case "closure_expression":
		return staticRustClosureUntypedParams(n, container, lang, src)
	case "let_declaration":
		return staticRustLetBindings(n, container, lang, src)
	case "for_expression":
		return staticRustForBinding(n, container, lang, src)
	}
	return nil
}

func staticRustStructFieldBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	if container == "" {
		return nil
	}
	nameNode := syntaxField(n, lang, "name")
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
	return []syntaxBinding{staticFieldBinding(container, name, typeText, n.StartByte(), n.EndByte())}
}

func staticRustParameterBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	patternNode := syntaxField(n, lang, "pattern")
	name := strings.TrimSpace(syntaxNodeText(patternNode, src))
	if name == "" {
		return nil
	}
	typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
	scopeStart, end := staticScopeBounds(staticEnclosingFunctionLike(n, lang), lang)
	if end == 0 {
		end = n.EndByte()
		scopeStart = n.StartByte()
	}
	return []syntaxBinding{staticLocalBinding(container, name, typeText, patternNode.StartByte(), end, scopeStart)}
}

// staticRustClosureUntypedParams handles bare "|x|" closure parameters,
// which the grammar represents as plain identifiers directly under
// closure_parameters rather than as "parameter" nodes (those are handled by
// staticRustParameterBinding when the walk visits them directly).
func staticRustClosureUntypedParams(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	params := syntaxField(n, lang, "parameters")
	if params == nil {
		return nil
	}
	scopeStart, end := staticScopeBounds(n, lang)
	var bindings []syntaxBinding
	for i := 0; i < params.ChildCount(); i++ {
		child := params.Child(i)
		if syntaxNodeType(child, lang) != "identifier" {
			continue
		}
		name := strings.TrimSpace(syntaxNodeText(child, src))
		if name == "" {
			continue
		}
		bindings = append(bindings, staticLocalBinding(container, name, "", child.StartByte(), end, scopeStart))
	}
	return bindings
}

func staticRustLetBindings(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	pattern := syntaxField(n, lang, "pattern")
	if pattern == nil {
		return nil
	}
	scopeStart, end := staticEnclosingBlockBounds(n, lang)
	if syntaxNodeType(pattern, lang) == "identifier" {
		name := strings.TrimSpace(syntaxNodeText(pattern, src))
		if name == "" {
			return nil
		}
		typeText := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "type"), src))
		if typeText == "" {
			typeText = staticRustLetValueType(syntaxField(n, lang, "value"), lang, src)
		}
		return []syntaxBinding{staticLocalBinding(container, name, typeText, pattern.StartByte(), end, scopeStart)}
	}
	return staticRustPatternBindings(pattern, container, end, scopeStart, lang, src)
}

// staticRustLetValueType infers a local's type only from a struct literal
// initializer ("let x = Foo { .. }" -> "Foo"). Calls are deliberately not
// inferred here ("let x = Foo::new()" stays untyped): a builder or factory
// function can return any type, so its callee name is not reliable evidence.
func staticRustLetValueType(value *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if value == nil || syntaxNodeType(value, lang) != "struct_expression" {
		return ""
	}
	nameNode := syntaxField(value, lang, "name")
	if nameNode == nil {
		nameNode = syntaxField(value, lang, "type")
	}
	return strings.TrimSpace(syntaxNodeText(nameNode, src))
}

// staticRustPatternBindings covers destructuring patterns (tuples, struct
// patterns, tuple-struct patterns, match arms). Every bound identifier gets
// an untyped binding: inferring a field's type from the pattern shape would
// need to match it against the pattern's own (possibly absent) declared
// type, which this syntactic pass does not attempt.
func staticRustPatternBindings(pattern *gotreesitter.Node, container string, end, scopeStart uint32, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	var bindings []syntaxBinding
	var walk func(*gotreesitter.Node)
	walk = func(node *gotreesitter.Node) {
		if node == nil {
			return
		}
		if syntaxNodeType(node, lang) == "identifier" {
			name := strings.TrimSpace(syntaxNodeText(node, src))
			if name != "" {
				bindings = append(bindings, staticLocalBinding(container, name, "", node.StartByte(), end, scopeStart))
			}
			return
		}
		for i := 0; i < node.ChildCount(); i++ {
			walk(node.Child(i))
		}
	}
	walk(pattern)
	return bindings
}

func staticRustForBinding(n *gotreesitter.Node, container string, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	patternNode := syntaxField(n, lang, "pattern")
	if patternNode == nil {
		return nil
	}
	scopeStart, end := staticScopeBounds(n, lang)
	if syntaxNodeType(patternNode, lang) == "identifier" {
		name := strings.TrimSpace(syntaxNodeText(patternNode, src))
		if name == "" {
			return nil
		}
		return []syntaxBinding{staticLocalBinding(container, name, "", patternNode.StartByte(), end, scopeStart)}
	}
	return staticRustPatternBindings(patternNode, container, end, scopeStart, lang, src)
}

func staticRustHeritage(n *gotreesitter.Node, typ string, lang *gotreesitter.Language, src []byte) []syntaxHeritage {
	switch typ {
	case "impl_item":
		traitNode := syntaxField(n, lang, "trait")
		if traitNode == nil {
			return nil
		}
		typeNode := staticUnwrapGenericType(syntaxField(n, lang, "type"), lang)
		implName := staticStripGenericArgs(syntaxNodeText(typeNode, src))
		traitName := staticStripGenericArgs(syntaxNodeText(traitNode, src))
		if implName == "" || traitName == "" {
			return nil
		}
		return []syntaxHeritage{{Type: implName, Super: traitName, Kind: syntaxHeritageImplements}}
	case "trait_item":
		name := staticDeclarationSimpleName(n, lang, src)
		if name == "" {
			return nil
		}
		bounds := syntaxField(n, lang, "bounds")
		if bounds == nil {
			return nil
		}
		var heritage []syntaxHeritage
		for i := 0; i < bounds.ChildCount(); i++ {
			child := bounds.Child(i)
			if syntaxNodeType(child, lang) != "type_identifier" {
				continue
			}
			text := staticStripGenericArgs(syntaxNodeText(child, src))
			if text != "" {
				heritage = append(heritage, syntaxHeritage{Type: name, Super: text, Kind: syntaxHeritageExtends})
			}
		}
		return heritage
	}
	return nil
}
