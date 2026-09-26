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

type dynamicScope struct {
	name string
	kind string
}

func extractDynamicSyntax(ctx context.Context, root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, language string) (syntaxFacts, error) {
	if root == nil || lang == nil {
		return syntaxFacts{}, fmt.Errorf("extract %s syntax: missing syntax tree or grammar", language)
	}
	switch language {
	case "javascript", "typescript", "tsx", "python":
	default:
		return syntaxFacts{}, fmt.Errorf("extract dynamic syntax: unsupported grammar %q", language)
	}
	var facts syntaxFacts
	var visit func(*gotreesitter.Node, []dynamicScope, []*gotreesitter.Node) error
	visit = func(node *gotreesitter.Node, scopes []dynamicScope, blocks []*gotreesitter.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node == nil || node.IsError() || node.IsMissing() {
			return nil
		}
		typ := syntaxNodeType(node, lang)
		if language != "python" && dynamicIsJSScopeNode(typ) {
			blocks = append(blocks, node)
		}
		name := dynamicDeclarationName(node, lang, src, typ)
		kind := dynamicDeclarationKind(node, lang, typ, language, len(scopes) > 0 && scopes[len(scopes)-1].kind == "class")
		if kind != "" && name != "" {
			container := dynamicContainer(scopes)
			detail := syntaxHeader(node, lang, src, typ)
			interfaceFlag := dynamicIsInterfaceDeclaration(node, lang, src, typ, language)
			facts.Declarations = append(facts.Declarations, syntaxDeclaration{
				Name: name, Kind: kind, Container: container, Detail: detail,
				StartByte: node.StartByte(), EndByte: node.EndByte(),
				Interface: interfaceFlag,
			})
			scopes = append(scopes, dynamicScope{name: name, kind: dynamicScopeKind(typ, kind)})
			if kind == graph.KindType {
				childContainer := dynamicContainer(scopes)
				facts.Heritage = append(facts.Heritage, dynamicHeritage(node, lang, src, typ, language, childContainer)...)
			}
		}
		if language == "python" {
			if err := dynamicPythonImports(node, lang, src, &facts); err != nil {
				return err
			}
			facts.Bindings = append(facts.Bindings, dynamicPythonBindings(node, lang, src, scopes)...)
		} else {
			if err := dynamicJSImports(node, lang, src, &facts); err != nil {
				return err
			}
			if err := dynamicJSRuntimeImport(node, lang, src, &facts); err != nil {
				return err
			}
			facts.Bindings = append(facts.Bindings, dynamicJSBindings(node, lang, src, scopes, blocks, typ)...)
		}
		if target := dynamicCallTarget(node, lang, src, typ, language); target != "" {
			facts.Calls = append(facts.Calls, syntaxCall{Target: target, StartByte: node.StartByte()})
		}
		if language != "python" && typ == "new_expression" {
			if target := dynamicConstructorTarget(node, lang, src); target != "" {
				facts.Calls = append(facts.Calls, syntaxCall{Target: target, StartByte: node.StartByte(), Constructor: true})
			}
		}
		for i := 0; i < node.ChildCount(); i++ {
			if err := visit(node.Child(i), scopes, blocks); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, nil, nil); err != nil {
		return syntaxFacts{}, err
	}
	sort.SliceStable(facts.Declarations, func(i, j int) bool {
		if facts.Declarations[i].StartByte != facts.Declarations[j].StartByte {
			return facts.Declarations[i].StartByte < facts.Declarations[j].StartByte
		}
		return facts.Declarations[i].EndByte < facts.Declarations[j].EndByte
	})
	sort.SliceStable(facts.Calls, func(i, j int) bool {
		if facts.Calls[i].StartByte != facts.Calls[j].StartByte {
			return facts.Calls[i].StartByte < facts.Calls[j].StartByte
		}
		return facts.Calls[i].Target < facts.Calls[j].Target
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
	return facts, nil
}

func dynamicDeclarationName(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, typ string) string {
	switch typ {
	case "function_declaration", "function_definition", "generator_function_declaration", "class_declaration", "class_definition", "abstract_class_declaration",
		"interface_declaration", "type_alias_declaration", "enum_declaration", "method_definition",
		"method_signature", "abstract_method_signature":
		return strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "name"), src))
	case "variable_declarator":
		value := syntaxField(n, lang, "value")
		if value == nil {
			value = syntaxField(n, lang, "right")
		}
		if value != nil && (isDynamicFunction(syntaxNodeType(value, lang)) || isDynamicClassExpression(syntaxNodeType(value, lang))) {
			return strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "name"), src))
		}
	case "assignment":
		value := syntaxField(n, lang, "right")
		if value != nil && (isDynamicFunction(syntaxNodeType(value, lang)) || isDynamicClassExpression(syntaxNodeType(value, lang))) {
			return strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "left"), src))
		}
	}
	return ""
}

func dynamicDeclarationKind(n *gotreesitter.Node, lang *gotreesitter.Language, typ, language string, inClass bool) string {
	switch typ {
	case "class_declaration", "class_definition", "abstract_class_declaration", "interface_declaration", "type_alias_declaration", "enum_declaration":
		return graph.KindType
	case "function_declaration", "function_definition", "generator_function_declaration":
		if inClass {
			return graph.KindMethod
		}
		return graph.KindFunction
	case "method_definition", "method_signature", "abstract_method_signature":
		return graph.KindMethod
	case "variable_declarator", "assignment":
		value := syntaxField(n, lang, "value")
		if value == nil {
			value = syntaxField(n, lang, "right")
		}
		if value != nil {
			valueType := syntaxNodeType(value, lang)
			if isDynamicClassExpression(valueType) {
				return graph.KindType
			}
			if isDynamicFunction(valueType) {
				if inClass {
					return graph.KindMethod
				}
				return graph.KindFunction
			}
		}
	}
	return ""
}

func isDynamicFunction(typ string) bool {
	return typ == "arrow_function" || typ == "function_expression" || typ == "generator_function" || typ == "lambda"
}

// isDynamicClassExpression reports whether typ is a JS/TS anonymous class
// expression (`class extends Base {}`), the value shape a variable_declarator
// or assignment can bind to a name, e.g. `const D = class extends Base {}`.
func isDynamicClassExpression(typ string) bool {
	return typ == "class"
}

func dynamicScopeKind(typ, kind string) string {
	if kind == graph.KindType {
		return "class"
	}
	return "function"
}

func dynamicContainer(scopes []dynamicScope) string {
	parts := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		parts = append(parts, scope.name)
	}
	return strings.Join(parts, ".")
}

// dynamicClassContainer returns the Container string that a type declaration's
// own members carry: the scope chain truncated at the nearest enclosing class,
// dropping any function or class scopes nested inside that class. This is the
// same value a method of that class receives as its own Container, so field
// bindings recorded anywhere inside the class line up with it.
func dynamicClassContainer(scopes []dynamicScope) (string, bool) {
	lastClass := -1
	for i, scope := range scopes {
		if scope.kind == "class" {
			lastClass = i
		}
	}
	if lastClass < 0 {
		return "", false
	}
	return dynamicContainer(scopes[:lastClass+1]), true
}

func dynamicHasFunctionScope(scopes []dynamicScope) bool {
	for _, scope := range scopes {
		if scope.kind == "function" {
			return true
		}
	}
	return false
}

func syntaxHeader(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, typ string) string {
	end := n.EndByte()
	var findBody func(*gotreesitter.Node) bool
	findBody = func(current *gotreesitter.Node) bool {
		ct := syntaxNodeType(current, lang)
		if current != n && (ct == "statement_block" || ct == "block" || ct == "class_body") {
			end = current.StartByte()
			return true
		}
		for i := 0; i < current.ChildCount(); i++ {
			if findBody(current.Child(i)) {
				return true
			}
		}
		return false
	}
	findBody(n)
	if end < n.StartByte() || int(end) > len(src) {
		return typ
	}
	text := strings.TrimSpace(string(src[n.StartByte():end]))
	if newline := strings.IndexByte(text, '\n'); newline >= 0 {
		text = text[:newline]
	}
	if len(text) > 180 {
		text = text[:177] + "..."
	}
	return text
}

func dynamicCallTarget(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, typ, language string) string {
	want := "call_expression"
	if language == "python" {
		want = "call"
	}
	if typ != want {
		return ""
	}
	fn := syntaxField(n, lang, "function")
	return dynamicSourceName(fn, lang, src)
}

func dynamicSourceName(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil {
		return ""
	}
	typ := syntaxNodeType(n, lang)
	switch typ {
	case "identifier", "property_identifier", "private_property_identifier", "dotted_name":
		return strings.TrimSpace(syntaxNodeText(n, src))
	case "this", "super":
		return syntaxNodeText(n, src)
	case "member_expression", "optional_member_expression", "attribute":
		obj := syntaxField(n, lang, "object")
		if obj == nil {
			obj = syntaxField(n, lang, "value")
		}
		prop := syntaxField(n, lang, "property")
		if prop == nil {
			prop = syntaxField(n, lang, "attribute")
		}
		left, right := dynamicSourceName(obj, lang, src), dynamicSourceName(prop, lang, src)
		if left != "" && right != "" {
			return left + "." + right
		}
	}
	return ""
}

func dynamicJSImports(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, facts *syntaxFacts) error {
	typ := syntaxNodeType(n, lang)
	if typ == "import_statement" {
		path := dynamicString(syntaxField(n, lang, "source"), lang, src)
		clause := firstDynamicChild(n, lang, "import_clause")
		if path != "" {
			if clause == nil {
				facts.Imports = append(facts.Imports, syntaxImport{Path: path})
			} else {
				dynamicJSImportClause(clause, lang, src, path, facts)
			}
		}
	}
	if typ == "export_statement" {
		path := dynamicString(syntaxField(n, lang, "source"), lang, src)
		clause := firstDynamicChild(n, lang, "export_clause")
		if path != "" && clause != nil {
			for i := 0; i < clause.ChildCount(); i++ {
				spec := clause.Child(i)
				if syntaxNodeType(spec, lang) != "export_specifier" {
					continue
				}
				imported := strings.TrimSpace(syntaxNodeText(syntaxField(spec, lang, "name"), src))
				local := strings.TrimSpace(syntaxNodeText(syntaxField(spec, lang, "alias"), src))
				if local == "" {
					local = imported
				}
				if imported != "" {
					facts.Imports = append(facts.Imports, syntaxImport{Path: path, Local: local, Imported: imported})
				}
			}
		}
	}
	return nil
}

func dynamicJSRuntimeImport(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, facts *syntaxFacts) error {
	typ := syntaxNodeType(n, lang)
	if typ == "import_type" {
		path := dynamicFirstLiteral(n, lang, src)
		if path != "" && !dynamicHasImportPath(facts.Imports, path) {
			facts.Imports = append(facts.Imports, syntaxImport{Path: path})
		}
		return nil
	}
	if typ != "call_expression" {
		return nil
	}
	fn := syntaxField(n, lang, "function")
	name := strings.TrimSpace(syntaxNodeText(fn, src))
	if name != "require" && name != "import" {
		return nil
	}
	args := syntaxField(n, lang, "arguments")
	if args == nil || args.ChildCount() == 0 {
		return nil
	}
	var firstArgument *gotreesitter.Node
	for i := 0; i < args.ChildCount(); i++ {
		if args.Child(i).IsNamed() {
			firstArgument = args.Child(i)
			break
		}
	}
	path := dynamicFirstLiteral(firstArgument, lang, src)
	if path == "" {
		return nil
	}
	local := ""
	if name == "require" && n.Parent() != nil && syntaxNodeType(n.Parent(), lang) == "variable_declarator" {
		local = strings.TrimSpace(syntaxNodeText(syntaxField(n.Parent(), lang, "name"), src))
	}
	for _, imp := range facts.Imports {
		if imp.Path == path && imp.Local == local && imp.Imported == "" {
			return nil
		}
	}
	facts.Imports = append(facts.Imports, syntaxImport{Path: path, Local: local})
	return nil
}

func dynamicFirstLiteral(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil {
		return ""
	}
	if path := dynamicString(n, lang, src); path != "" {
		return path
	}
	for i := 0; i < n.ChildCount(); i++ {
		if path := dynamicFirstLiteral(n.Child(i), lang, src); path != "" {
			return path
		}
	}
	return ""
}

func dynamicHasImportPath(imports []syntaxImport, path string) bool {
	for _, imp := range imports {
		if imp.Path == path {
			return true
		}
	}
	return false
}

func dynamicConstructorTarget(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	for _, field := range []string{"constructor", "function", "constructor_type"} {
		if child := syntaxField(n, lang, field); child != nil {
			if name := dynamicSourceName(child, lang, src); name != "" {
				return name
			}
			if name := strings.TrimSpace(syntaxNodeText(child, src)); name != "" {
				return name
			}
		}
	}
	for i := 0; i < n.ChildCount(); i++ {
		child := n.Child(i)
		switch syntaxNodeType(child, lang) {
		case "arguments", "type_arguments":
			continue
		}
		if name := dynamicSourceName(child, lang, src); name != "" {
			return name
		}
	}
	return ""
}

func dynamicConstructedType(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil || syntaxNodeType(n, lang) != "new_expression" {
		return ""
	}
	return dynamicConstructorTarget(n, lang, src)
}

// dynamicReceiverField reports the member name accessed off a bare receiver
// identifier (this.x in JS/TS, self.x in Python), or "" when n is not such an
// access.
func dynamicReceiverField(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, receiver string) string {
	if n == nil {
		return ""
	}
	switch syntaxNodeType(n, lang) {
	case "member_expression", "member_access_expression", "attribute":
	default:
		return ""
	}
	object := syntaxField(n, lang, "object")
	if strings.TrimSpace(syntaxNodeText(object, src)) != receiver {
		return ""
	}
	prop := syntaxField(n, lang, "property")
	if prop == nil {
		prop = syntaxField(n, lang, "attribute")
	}
	return strings.TrimSpace(syntaxNodeText(prop, src))
}

// dynamicAnnotationType extracts the syntactic type text from a TS/JS
// type_annotation node (the ": Foo" suffix on a declaration), returning "" for
// union types since a union does not name a single class.
func dynamicAnnotationType(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil {
		return ""
	}
	var inner *gotreesitter.Node
	for i := 0; i < n.ChildCount(); i++ {
		if n.Child(i).IsNamed() {
			inner = n.Child(i)
			break
		}
	}
	if inner == nil {
		return ""
	}
	if syntaxNodeType(inner, lang) == "union_type" {
		return dynamicNullableUnionType(inner, lang, src)
	}
	return strings.TrimSpace(syntaxNodeText(inner, src))
}

// dynamicNullableUnionType collapses a two-member TS union where one member
// is the null or undefined literal type to the other member's type text
// (`Foo | null` -> "Foo"), matching how an optional field still names one
// class. Any other union shape (three or more members, or no null/undefined
// member) does not name a single class and yields "".
func dynamicNullableUnionType(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	var members []*gotreesitter.Node
	for i := 0; i < n.ChildCount(); i++ {
		if n.Child(i).IsNamed() {
			members = append(members, n.Child(i))
		}
	}
	if len(members) != 2 {
		return ""
	}
	isNullish := func(m *gotreesitter.Node) bool {
		switch strings.TrimSpace(syntaxNodeText(m, src)) {
		case "null", "undefined":
			return true
		}
		return false
	}
	switch {
	case isNullish(members[0]) && !isNullish(members[1]):
		return strings.TrimSpace(syntaxNodeText(members[1], src))
	case isNullish(members[1]) && !isNullish(members[0]):
		return strings.TrimSpace(syntaxNodeText(members[0], src))
	}
	return ""
}

func dynamicHasParameterPropertyModifier(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) bool {
	var found bool
	var visit func(*gotreesitter.Node)
	visit = func(current *gotreesitter.Node) {
		if current == nil || found {
			return
		}
		if syntaxNodeType(current, lang) == "accessibility_modifier" {
			switch strings.TrimSpace(syntaxNodeText(current, src)) {
			case "public", "private", "protected":
				found = true
				return
			}
		}
		for i := 0; i < current.ChildCount(); i++ {
			visit(current.Child(i))
		}
	}
	visit(n)
	return found
}

func dynamicDescendantField(n *gotreesitter.Node, lang *gotreesitter.Language, field string) *gotreesitter.Node {
	if result := syntaxField(n, lang, field); result != nil {
		return result
	}
	for i := 0; i < n.ChildCount(); i++ {
		if result := dynamicDescendantField(n.Child(i), lang, field); result != nil {
			return result
		}
	}
	return nil
}

func dynamicJSImportClause(clause *gotreesitter.Node, lang *gotreesitter.Language, src []byte, path string, facts *syntaxFacts) {
	for i := 0; i < clause.ChildCount(); i++ {
		child := clause.Child(i)
		typ := syntaxNodeType(child, lang)
		switch typ {
		case "identifier":
			local := strings.TrimSpace(syntaxNodeText(child, src))
			facts.Imports = append(facts.Imports, syntaxImport{Path: path, Local: local, Imported: "default"})
		case "namespace_import":
			local := dynamicLastIdentifier(child, lang, src)
			if local != "" {
				facts.Imports = append(facts.Imports, syntaxImport{Path: path, Local: local, Imported: "*"})
			}
		case "named_imports":
			for j := 0; j < child.ChildCount(); j++ {
				spec := child.Child(j)
				if syntaxNodeType(spec, lang) != "import_specifier" {
					continue
				}
				imported := strings.TrimSpace(syntaxNodeText(syntaxField(spec, lang, "name"), src))
				local := strings.TrimSpace(syntaxNodeText(syntaxField(spec, lang, "alias"), src))
				if local == "" {
					local = imported
				}
				if imported != "" {
					facts.Imports = append(facts.Imports, syntaxImport{Path: path, Local: local, Imported: imported})
				}
			}
		}
	}
}

func dynamicPythonImports(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, facts *syntaxFacts) error {
	typ := syntaxNodeType(n, lang)
	if typ != "import_statement" && typ != "import_from_statement" {
		return nil
	}
	path := ""
	if typ == "import_from_statement" {
		path = strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "module_name"), src))
		if path == "" {
			for i := 0; i < n.ChildCount(); i++ {
				ct := syntaxNodeType(n.Child(i), lang)
				if ct == "dotted_name" || ct == "relative_import" {
					path = strings.TrimSpace(syntaxNodeText(n.Child(i), src))
					break
				}
			}
		}
	}
	for i := 0; i < n.ChildCount(); i++ {
		child := n.Child(i)
		ct := syntaxNodeType(child, lang)
		if ct != "aliased_import" && ct != "dotted_name" && ct != "wildcard_import" && ct != "identifier" {
			continue
		}
		if typ == "import_from_statement" && (ct == "dotted_name" || ct == "relative_import") && syntaxNodeText(child, src) == path {
			continue
		}
		imported, local := strings.TrimSpace(syntaxNodeText(child, src)), ""
		if ct == "aliased_import" {
			name := syntaxField(child, lang, "name")
			alias := syntaxField(child, lang, "alias")
			imported = strings.TrimSpace(syntaxNodeText(name, src))
			local = strings.TrimSpace(syntaxNodeText(alias, src))
			if local == "" {
				local = imported
			}
		} else {
			local = imported
		}
		if imported == "" {
			continue
		}
		if typ == "import_statement" {
			path, imported = imported, ""
		}
		facts.Imports = append(facts.Imports, syntaxImport{Path: path, Local: local, Imported: imported})
	}
	return nil
}

func dynamicString(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil {
		return ""
	}
	t := strings.TrimSpace(syntaxNodeText(n, src))
	if len(t) >= 2 && ((t[0] == '\'' && t[len(t)-1] == '\'') || (t[0] == '"' && t[len(t)-1] == '"') || (t[0] == '`' && t[len(t)-1] == '`')) {
		return t[1 : len(t)-1]
	}
	if syntaxNodeType(n, lang) == "string" {
		return ""
	}
	return ""
}

func dynamicLastIdentifier(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	var found string
	_ = walkSyntaxNamed(context.Background(), n, func(node *gotreesitter.Node) bool {
		if syntaxNodeType(node, lang) == "identifier" {
			found = strings.TrimSpace(syntaxNodeText(node, src))
		}
		return true
	})
	return found
}

func firstDynamicChild(n *gotreesitter.Node, lang *gotreesitter.Language, typ string) *gotreesitter.Node {
	for i := 0; i < n.ChildCount(); i++ {
		if syntaxNodeType(n.Child(i), lang) == typ {
			return n.Child(i)
		}
	}
	return nil
}

// dynamicIsInterfaceDeclaration reports whether a type declaration behaves as
// an interface for supertype classification: TS/TSX interfaces, and Python
// classes that directly list Protocol or typing.Protocol as a base.
func dynamicIsInterfaceDeclaration(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, typ, language string) bool {
	if typ == "interface_declaration" {
		return true
	}
	if language != "python" || typ != "class_definition" {
		return false
	}
	for _, super := range dynamicPythonHeritageSupers(n, lang, src) {
		if super == "Protocol" || super == "typing.Protocol" {
			return true
		}
	}
	return false
}

func dynamicHeritage(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, typ, language, container string) []syntaxHeritage {
	var result []syntaxHeritage
	switch {
	case language == "python" && typ == "class_definition":
		for _, super := range dynamicPythonHeritageSupers(n, lang, src) {
			result = append(result, syntaxHeritage{Type: container, Super: super})
		}
	case typ == "interface_declaration":
		for _, super := range dynamicTSInterfaceHeritageSupers(n, lang, src) {
			result = append(result, syntaxHeritage{Type: container, Super: super, Kind: syntaxHeritageExtends})
		}
	case typ == "class_declaration" || typ == "abstract_class_declaration":
		for _, h := range dynamicTSClassHeritageSupers(n, lang, src) {
			h.Type = container
			result = append(result, h)
		}
	case typ == "variable_declarator" || typ == "assignment":
		value := syntaxField(n, lang, "value")
		if value == nil {
			value = syntaxField(n, lang, "right")
		}
		if value != nil && isDynamicClassExpression(syntaxNodeType(value, lang)) {
			for _, h := range dynamicTSClassHeritageSupers(value, lang, src) {
				h.Type = container
				result = append(result, h)
			}
		}
	}
	return result
}

func dynamicTSTypeText(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil {
		return ""
	}
	if syntaxNodeType(n, lang) == "generic_type" {
		return dynamicTSTypeText(syntaxField(n, lang, "name"), lang, src)
	}
	return strings.TrimSpace(syntaxNodeText(n, src))
}

func dynamicTSInterfaceHeritageSupers(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []string {
	clause := firstDynamicChild(n, lang, "extends_type_clause")
	if clause == nil {
		return nil
	}
	var supers []string
	for i := 0; i < clause.ChildCount(); i++ {
		if clause.FieldNameForChild(i, lang) != "type" {
			continue
		}
		if text := dynamicTSTypeText(clause.Child(i), lang, src); text != "" {
			supers = append(supers, text)
		}
	}
	return supers
}

func dynamicTSClassHeritageSupers(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []syntaxHeritage {
	var result []syntaxHeritage
	heritage := firstDynamicChild(n, lang, "class_heritage")
	if heritage == nil {
		return result
	}
	if extendsClause := firstDynamicChild(heritage, lang, "extends_clause"); extendsClause != nil {
		// TypeScript grammar shape: class_heritage wraps an extends_clause
		// with a "value" field.
		if text := dynamicTSTypeText(syntaxField(extendsClause, lang, "value"), lang, src); text != "" {
			result = append(result, syntaxHeritage{Super: text, Kind: syntaxHeritageExtends})
		}
	} else if text := dynamicJSHeritageExtendsSuper(heritage, lang, src); text != "" {
		// Plain JavaScript grammar shape: class_heritage directly holds the
		// "extends" keyword followed by the superclass expression, with no
		// extends_clause wrapper.
		result = append(result, syntaxHeritage{Super: text, Kind: syntaxHeritageExtends})
	}
	if implementsClause := firstDynamicChild(heritage, lang, "implements_clause"); implementsClause != nil {
		for i := 0; i < implementsClause.ChildCount(); i++ {
			child := implementsClause.Child(i)
			if !child.IsNamed() {
				continue
			}
			if text := dynamicTSTypeText(child, lang, src); text != "" {
				result = append(result, syntaxHeritage{Super: text, Kind: syntaxHeritageImplements})
			}
		}
	}
	return result
}

// dynamicJSHeritageExtendsSuper returns the superclass expression text of a
// plain JavaScript class_heritage node (no extends_clause wrapper): its only
// named child is the expression named after the "extends" keyword.
func dynamicJSHeritageExtendsSuper(heritage *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	for i := 0; i < heritage.ChildCount(); i++ {
		child := heritage.Child(i)
		if !child.IsNamed() {
			continue
		}
		if text := dynamicTSTypeText(child, lang, src); text != "" {
			return text
		}
	}
	return ""
}

func dynamicPythonHeritageSupers(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []string {
	args := syntaxField(n, lang, "superclasses")
	if args == nil {
		return nil
	}
	var supers []string
	for i := 0; i < args.ChildCount(); i++ {
		child := args.Child(i)
		if !child.IsNamed() {
			continue
		}
		switch syntaxNodeType(child, lang) {
		case "identifier", "attribute":
			text := strings.TrimSpace(syntaxNodeText(child, src))
			if text != "" && text != "object" {
				supers = append(supers, text)
			}
		case "subscript":
			text := strings.TrimSpace(syntaxNodeText(syntaxField(child, lang, "value"), src))
			if text != "" {
				supers = append(supers, text)
			}
		}
	}
	return supers
}

// dynamicIsJSScopeNode reports whether typ introduces a JS/TS scope that a
// reassignment's declaring binding can live in: a block-like scope (matching
// dynamicEnclosingScope's stop set) or a function-like node's own
// parameter scope. Tracking these top-down while walking the tree avoids
// gotreesitter's Parent() links, which do not climb back out through the
// grammar's hidden single-statement wrapper used for if/while/for bodies.
func dynamicIsJSScopeNode(typ string) bool {
	switch typ {
	case "statement_block", "program", "class_static_block", "for_statement",
		"function_declaration", "function_expression", "arrow_function", "method_definition",
		"generator_function_declaration", "generator_function":
		return true
	}
	return false
}

// dynamicJSBindings extracts field and local bindings produced by a single
// JS/TS node, keyed on its node type. blocks is the chain of enclosing
// dynamicIsJSScopeNode ancestors, outermost first, used to resolve
// reassignment scoping.
func dynamicJSBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope, blocks []*gotreesitter.Node, typ string) []syntaxBinding {
	switch typ {
	case "public_field_definition", "property_definition":
		return dynamicClassFieldBinding(n, lang, src, scopes)
	case "assignment_expression":
		return dynamicAssignmentBindings(n, lang, src, scopes, blocks)
	case "augmented_assignment_expression":
		return dynamicAugmentedAssignmentBindings(n, lang, src, blocks)
	case "required_parameter", "optional_parameter", "parameter":
		return dynamicParameterPropertyBinding(n, lang, src, scopes)
	case "function_declaration", "method_definition", "arrow_function", "function_expression",
		"generator_function_declaration", "generator_function":
		return dynamicFunctionParamBindings(n, lang, src)
	case "variable_declarator":
		return dynamicVariableDeclaratorBindings(n, lang, src, blocks)
	case "for_in_statement":
		return dynamicForInBindings(n, lang, src, blocks)
	case "catch_clause":
		return dynamicCatchBindings(n, lang, src)
	}
	return nil
}

func dynamicClassFieldBinding(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope) []syntaxBinding {
	container, ok := dynamicClassContainer(scopes)
	if !ok {
		return nil
	}
	name := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "name"), src))
	if name == "" {
		return nil
	}
	valueNode := syntaxField(n, lang, "value")
	fieldType := dynamicAnnotationType(syntaxField(n, lang, "type"), lang, src)
	if fieldType == "" {
		fieldType = dynamicConstructedType(valueNode, lang, src)
	}
	if fieldType == "" {
		if valueNode == nil || dynamicIsNullishLiteral(valueNode, lang) {
			return nil
		}
	}
	return []syntaxBinding{{Container: container, Field: name, Type: fieldType}}
}

// dynamicIsNullishLiteral reports whether n is a bare null/undefined literal,
// the common `x = null` / `x = undefined` placeholder used to declare a
// field before its real type is assigned elsewhere (typically in a
// constructor or setter). Such placeholders must not shadow a real
// constructor assignment with an unknown-typed field binding.
func dynamicIsNullishLiteral(n *gotreesitter.Node, lang *gotreesitter.Language) bool {
	if n == nil {
		return false
	}
	switch syntaxNodeType(n, lang) {
	case "null", "undefined":
		return true
	}
	return false
}

func dynamicThisConstructorBinding(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope) []syntaxBinding {
	container, ok := dynamicClassContainer(scopes)
	if !ok {
		return nil
	}
	left, right := syntaxField(n, lang, "left"), syntaxField(n, lang, "right")
	fieldName := dynamicReceiverField(left, lang, src, "this")
	constructedType := dynamicConstructedType(right, lang, src)
	if fieldName == "" || constructedType == "" {
		return nil
	}
	return []syntaxBinding{{Container: container, Field: fieldName, Type: constructedType}}
}

// dynamicAssignmentBindings extracts the bindings produced by a plain `=`
// assignment_expression: a `this.x = new Foo()` constructor assignment feeds
// a class field binding, and a bare identifier reassignment (`x = new
// Foo()`) feeds a Local binding scoped to end where its declaring binding
// ends, so the resolver can see it replaces the earlier declaration.
func dynamicAssignmentBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope, blocks []*gotreesitter.Node) []syntaxBinding {
	left := syntaxField(n, lang, "left")
	if left != nil && syntaxNodeType(left, lang) == "identifier" {
		return dynamicJSReassignmentBinding(n, lang, src, blocks, left, syntaxField(n, lang, "right"))
	}
	return dynamicThisConstructorBinding(n, lang, src, scopes)
}

// dynamicAugmentedAssignmentBindings extracts the Local binding produced by
// a compound reassignment of a bare identifier (`x += 1`, `x ??= new Foo()`).
// The Type is always "" since a compound assignment only ever combines with
// the existing value rather than replacing it with a syntactically known
// constructed type.
func dynamicAugmentedAssignmentBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, blocks []*gotreesitter.Node) []syntaxBinding {
	left := syntaxField(n, lang, "left")
	if left == nil || syntaxNodeType(left, lang) != "identifier" {
		return nil
	}
	return dynamicJSReassignmentBinding(n, lang, src, blocks, left, nil)
}

// dynamicJSReassignmentBinding builds the Local binding for reassigning an
// already-declared bare identifier. Its ScopeStart/EndByte match the
// declaring binding's own scope (found via dynamicDeclaredScope), so the
// resolver can pair the reassignment with its declaration and see the type
// change (or become unknown). An undeclared/implicit-global name falls back
// to the nearest enclosing function.
func dynamicJSReassignmentBinding(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, blocks []*gotreesitter.Node, left, right *gotreesitter.Node) []syntaxBinding {
	name := strings.TrimSpace(syntaxNodeText(left, src))
	if name == "" {
		return nil
	}
	typeText := dynamicConstructedType(right, lang, src)
	scope := dynamicDeclaredScope(n, blocks, lang, src, name)
	return []syntaxBinding{{Field: name, Type: typeText, Local: true, StartByte: n.StartByte(), EndByte: scope.EndByte(), ScopeStart: scope.StartByte()}}
}

// dynamicDeclaredScope finds the nearest enclosing scope node (block or
// function) that declares name, whose StartByte/EndByte must match the
// ScopeStart/EndByte the declaring binding itself carries. blocks is the
// chain of enclosing dynamicIsJSScopeNode ancestors accumulated top-down by
// extractDynamicSyntax (innermost last); this is used instead of walking
// gotreesitter's Parent() links outward because those links do not climb
// back out through the grammar's hidden single-statement wrapper used for
// if/while/for bodies. A `let`/`const` declaration only matches at the block
// level that directly contains it (dynamicBlockDeclaresLexicalName); a `var`
// declaration matches at the nearest enclosing function, class static block,
// or program regardless of how deeply nested inside intervening
// blocks/for-loops it is, since `var` hoists through them
// (dynamicScopeDeclaresVarName). Falls back to the nearest enclosing
// function for an implicit global/undeclared name, or n itself when there is
// no enclosing scope at all.
func dynamicDeclaredScope(n *gotreesitter.Node, blocks []*gotreesitter.Node, lang *gotreesitter.Language, src []byte, name string) *gotreesitter.Node {
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		switch syntaxNodeType(block, lang) {
		case "statement_block", "program", "class_static_block", "for_statement":
			if dynamicBlockDeclaresLexicalName(block, lang, src, name) {
				return block
			}
		case "function_declaration", "function_expression", "arrow_function", "method_definition",
			"generator_function_declaration", "generator_function":
			if dynamicFunctionDeclaresParam(block, lang, src, name) {
				return block
			}
		}
		switch syntaxNodeType(block, lang) {
		case "function_declaration", "function_expression", "arrow_function", "method_definition",
			"generator_function_declaration", "generator_function", "program", "class_static_block":
			if dynamicScopeDeclaresVarName(block, lang, src, name) {
				return block
			}
		}
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		switch syntaxNodeType(blocks[i], lang) {
		case "function_declaration", "function_expression", "arrow_function", "method_definition",
			"generator_function_declaration", "generator_function":
			return blocks[i]
		}
	}
	if len(blocks) > 0 {
		return blocks[0]
	}
	return n
}

// dynamicDeclaratorIsVarKind reports whether a variable_declarator's
// immediate parent is a `var` variable_declaration (function-scoped/hoisted)
// rather than a `let`/`const` lexical_declaration (block-scoped).
func dynamicDeclaratorIsVarKind(n *gotreesitter.Node, lang *gotreesitter.Language) bool {
	parent := n.Parent()
	return parent != nil && syntaxNodeType(parent, lang) == "variable_declaration"
}

// dynamicBlockDeclaresLexicalName reports whether block directly declares
// name via a let/const declarator reachable without crossing into a nested
// block, function, or class scope. `var` declarators are excluded here:
// their scope is resolved separately by dynamicScopeDeclaresVarName, since
// `var` hoists to the enclosing function/program regardless of block
// nesting.
func dynamicBlockDeclaresLexicalName(block *gotreesitter.Node, lang *gotreesitter.Language, src []byte, name string) bool {
	found := false
	var walk func(*gotreesitter.Node, bool)
	walk = func(cur *gotreesitter.Node, isRoot bool) {
		if cur == nil || found {
			return
		}
		if !isRoot {
			switch syntaxNodeType(cur, lang) {
			case "statement_block", "program", "class_static_block", "for_statement",
				"function_declaration", "function_expression", "arrow_function", "method_definition",
				"generator_function_declaration", "generator_function",
				"class_declaration", "abstract_class_declaration", "class":
				return
			}
		}
		if syntaxNodeType(cur, lang) == "variable_declarator" && !dynamicDeclaratorIsVarKind(cur, lang) {
			for _, declared := range dynamicPatternNames(syntaxField(cur, lang, "name"), lang, src) {
				if declared == name {
					found = true
					return
				}
			}
		}
		for i := 0; i < cur.ChildCount(); i++ {
			walk(cur.Child(i), false)
		}
	}
	walk(block, true)
	return found
}

// dynamicScopeDeclaresVarName reports whether scope (a var-hoisting
// boundary: function, program, or class static block) declares name via a
// `var` declarator anywhere within it. The walk crosses nested
// statement_block and for_statement boundaries, since `var` hoists through
// them, but stops at a nested function or class boundary, which starts its
// own hoisting scope.
func dynamicScopeDeclaresVarName(scope *gotreesitter.Node, lang *gotreesitter.Language, src []byte, name string) bool {
	found := false
	var walk func(*gotreesitter.Node, bool)
	walk = func(cur *gotreesitter.Node, isRoot bool) {
		if cur == nil || found {
			return
		}
		if !isRoot {
			switch syntaxNodeType(cur, lang) {
			case "function_declaration", "function_expression", "arrow_function", "method_definition",
				"generator_function_declaration", "generator_function",
				"class_declaration", "abstract_class_declaration", "class":
				return
			}
		}
		if syntaxNodeType(cur, lang) == "variable_declarator" && dynamicDeclaratorIsVarKind(cur, lang) {
			for _, declared := range dynamicPatternNames(syntaxField(cur, lang, "name"), lang, src) {
				if declared == name {
					found = true
					return
				}
			}
		}
		for i := 0; i < cur.ChildCount(); i++ {
			walk(cur.Child(i), false)
		}
	}
	walk(scope, true)
	return found
}

func dynamicFunctionDeclaresParam(fn *gotreesitter.Node, lang *gotreesitter.Language, src []byte, name string) bool {
	for _, binding := range dynamicFunctionParamBindings(fn, lang, src) {
		if binding.Field == name {
			return true
		}
	}
	return false
}

func dynamicParameterPropertyBinding(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope) []syntaxBinding {
	container, ok := dynamicClassContainer(scopes)
	if !ok {
		return nil
	}
	if !dynamicHasParameterPropertyModifier(n, lang, src) {
		return nil
	}
	nameNode := dynamicDescendantField(n, lang, "pattern")
	if nameNode == nil {
		nameNode = dynamicDescendantField(n, lang, "name")
	}
	name := strings.TrimSpace(syntaxNodeText(nameNode, src))
	fieldType := dynamicAnnotationType(syntaxField(n, lang, "type"), lang, src)
	if name == "" || fieldType == "" {
		return nil
	}
	return []syntaxBinding{{Container: container, Field: name, Type: fieldType}}
}

// dynamicFunctionParamBindings extracts a Local binding for every parameter of
// a JS/TS function-like node, scoped to the node's own byte range (its body
// always ends where the node itself ends).
func dynamicFunctionParamBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	scopeStart, end := n.StartByte(), n.EndByte()
	if single := syntaxField(n, lang, "parameter"); single != nil {
		return dynamicPatternBindings(single, lang, src, "", scopeStart, end)
	}
	params := syntaxField(n, lang, "parameters")
	if params == nil {
		return nil
	}
	var bindings []syntaxBinding
	for i := 0; i < params.ChildCount(); i++ {
		child := params.Child(i)
		if !child.IsNamed() {
			continue
		}
		bindings = append(bindings, dynamicParamChildBindings(child, lang, src, scopeStart, end)...)
	}
	return bindings
}

func dynamicParamChildBindings(child *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopeStart, end uint32) []syntaxBinding {
	switch syntaxNodeType(child, lang) {
	case "required_parameter", "optional_parameter":
		pattern := syntaxField(child, lang, "pattern")
		typeText := dynamicAnnotationType(syntaxField(child, lang, "type"), lang, src)
		return dynamicPatternBindings(pattern, lang, src, typeText, scopeStart, end)
	case "assignment_pattern":
		return dynamicPatternBindings(syntaxField(child, lang, "left"), lang, src, "", scopeStart, end)
	case "identifier", "object_pattern", "array_pattern", "rest_pattern":
		return dynamicPatternBindings(child, lang, src, "", scopeStart, end)
	}
	return nil
}

// dynamicPatternBindings turns a parameter/variable name pattern into Local
// bindings. A plain identifier keeps the caller-provided type; any
// destructuring pattern expands to one binding per leaf name with Type "".
// scopeStart is the StartByte of the scope node whose EndByte is end.
func dynamicPatternBindings(pattern *gotreesitter.Node, lang *gotreesitter.Language, src []byte, typeText string, scopeStart, end uint32) []syntaxBinding {
	if pattern == nil {
		return nil
	}
	if syntaxNodeType(pattern, lang) == "identifier" {
		name := strings.TrimSpace(syntaxNodeText(pattern, src))
		if name == "" {
			return nil
		}
		return []syntaxBinding{{Field: name, Type: typeText, Local: true, StartByte: pattern.StartByte(), EndByte: end, ScopeStart: scopeStart}}
	}
	var bindings []syntaxBinding
	for _, name := range dynamicPatternNames(pattern, lang, src) {
		bindings = append(bindings, syntaxBinding{Field: name, Type: "", Local: true, StartByte: pattern.StartByte(), EndByte: end, ScopeStart: scopeStart})
	}
	return bindings
}

// dynamicPatternNames flattens a JS/TS destructuring pattern (object, array,
// nested, with defaults and rest elements) into its leaf binding names.
func dynamicPatternNames(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []string {
	if n == nil {
		return nil
	}
	var names []string
	var walk func(*gotreesitter.Node)
	walk = func(cur *gotreesitter.Node) {
		if cur == nil {
			return
		}
		switch syntaxNodeType(cur, lang) {
		case "identifier", "shorthand_property_identifier_pattern":
			if name := strings.TrimSpace(syntaxNodeText(cur, src)); name != "" {
				names = append(names, name)
			}
			return
		case "pair_pattern":
			walk(syntaxField(cur, lang, "value"))
			return
		case "assignment_pattern":
			walk(syntaxField(cur, lang, "left"))
			return
		}
		for i := 0; i < cur.ChildCount(); i++ {
			walk(cur.Child(i))
		}
	}
	walk(n)
	return names
}

func dynamicVariableDeclaratorBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, blocks []*gotreesitter.Node) []syntaxBinding {
	valueNode := syntaxField(n, lang, "value")
	if valueNode != nil && isDynamicFunction(syntaxNodeType(valueNode, lang)) {
		return nil
	}
	namePattern := syntaxField(n, lang, "name")
	if namePattern == nil {
		return nil
	}
	var scope *gotreesitter.Node
	if dynamicDeclaratorIsVarKind(n, lang) {
		scope = dynamicEnclosingVarScope(n, lang, blocks)
	} else {
		scope = dynamicEnclosingScope(n, lang)
	}
	typeText := dynamicAnnotationType(syntaxField(n, lang, "type"), lang, src)
	if typeText == "" {
		typeText = dynamicConstructedType(valueNode, lang, src)
	}
	return dynamicPatternBindings(namePattern, lang, src, typeText, scope.StartByte(), scope.EndByte())
}

// dynamicEnclosingScope finds the nearest enclosing block scope node for a
// JS/TS `let`/`const` local declaration: the block/program it lives in, or
// the for-statement it initializes (`for (let i = 0; ...)` scopes i to the
// loop, not the surrounding block).
func dynamicEnclosingScope(n *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch syntaxNodeType(cur, lang) {
		case "statement_block", "program", "class_static_block", "for_statement":
			return cur
		}
	}
	return n
}

// dynamicEnclosingVarScope finds the nearest enclosing var-hoisting scope
// node for a JS/TS `var` declarator: the innermost enclosing function or
// class static block, or the program when there is none. `var` hoists
// through intervening block/for-loop boundaries, unlike let/const. blocks is
// the chain of enclosing dynamicIsJSScopeNode ancestors accumulated top-down
// by extractDynamicSyntax (innermost last); this is used instead of walking
// gotreesitter's Parent() links outward because those links do not climb
// back out through the grammar's hidden single-statement wrapper used for
// if/while/for bodies once more than one hop is needed to reach the
// enclosing function.
func dynamicEnclosingVarScope(n *gotreesitter.Node, lang *gotreesitter.Language, blocks []*gotreesitter.Node) *gotreesitter.Node {
	for i := len(blocks) - 1; i >= 0; i-- {
		switch syntaxNodeType(blocks[i], lang) {
		case "function_declaration", "function_expression", "arrow_function", "method_definition",
			"generator_function_declaration", "generator_function", "class_static_block", "program":
			return blocks[i]
		}
	}
	return n
}

// dynamicForInBindings extracts the Local binding for a `for (kind x of/in
// ...)` loop head. A `var`-kind head is function-scoped like any other var
// (dynamicEnclosingVarScope), so it pairs with an earlier or later `var`
// declaration of the same name in the enclosing function/program rather than
// being confined to the loop. A `let`/`const`-kind head stays scoped to the
// loop itself.
func dynamicForInBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, blocks []*gotreesitter.Node) []syntaxBinding {
	kind := syntaxField(n, lang, "kind")
	if kind == nil {
		return nil
	}
	left := syntaxField(n, lang, "left")
	if left == nil {
		return nil
	}
	if strings.TrimSpace(syntaxNodeText(kind, src)) == "var" {
		scope := dynamicEnclosingVarScope(n, lang, blocks)
		return dynamicPatternBindings(left, lang, src, "", scope.StartByte(), scope.EndByte())
	}
	return dynamicPatternBindings(left, lang, src, "", n.StartByte(), n.EndByte())
}

func dynamicCatchBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	param := syntaxField(n, lang, "parameter")
	if param == nil {
		return nil
	}
	typeText := dynamicAnnotationType(syntaxField(n, lang, "type"), lang, src)
	return dynamicPatternBindings(param, lang, src, typeText, n.StartByte(), n.EndByte())
}

// dynamicPythonBindings extracts field and local bindings produced by a
// single Python node, keyed on its node type.
func dynamicPythonBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope) []syntaxBinding {
	switch syntaxNodeType(n, lang) {
	case "assignment":
		return dynamicPythonAssignmentBindings(n, lang, src, scopes)
	case "function_definition":
		return dynamicPythonParamBindings(n, lang, src)
	case "lambda":
		return dynamicPythonLambdaParamBindings(n, lang, src)
	case "for_statement":
		return dynamicPythonForBindings(n, lang, src, scopes)
	case "with_item":
		return dynamicPythonWithBindings(n, lang, src, scopes)
	case "except_clause":
		return dynamicPythonExceptBindings(n, lang, src, scopes)
	case "list_comprehension", "set_comprehension", "dictionary_comprehension", "generator_expression":
		return dynamicPythonComprehensionBindings(n, lang, src)
	}
	return nil
}

func dynamicPythonAssignmentBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope) []syntaxBinding {
	left := syntaxField(n, lang, "left")
	if left == nil {
		return nil
	}
	right := syntaxField(n, lang, "right")
	typeField := syntaxField(n, lang, "type")
	switch syntaxNodeType(left, lang) {
	case "attribute":
		name := dynamicReceiverField(left, lang, src, "self")
		if name == "" {
			return nil
		}
		container, ok := dynamicClassContainer(scopes)
		if !ok {
			return nil
		}
		fieldType := dynamicPythonValueType(right, typeField, lang, src)
		if fieldType == "" && dynamicPythonIsNonePlaceholder(right, lang) {
			return nil
		}
		return []syntaxBinding{{Container: container, Field: name, Type: fieldType}}
	case "identifier":
		name := strings.TrimSpace(syntaxNodeText(left, src))
		if name == "" {
			return nil
		}
		if len(scopes) > 0 && scopes[len(scopes)-1].kind == "class" {
			container, ok := dynamicClassContainer(scopes)
			if !ok {
				return nil
			}
			fieldType := dynamicPythonValueType(right, typeField, lang, src)
			if fieldType == "" && dynamicPythonIsNonePlaceholder(right, lang) {
				return nil
			}
			return []syntaxBinding{{Container: container, Field: name, Type: fieldType}}
		}
		if !dynamicHasFunctionScope(scopes) {
			return nil
		}
		scope := dynamicEnclosingFunction(n, lang)
		return []syntaxBinding{{Field: name, Type: dynamicPythonValueType(right, typeField, lang, src), Local: true, StartByte: n.StartByte(), EndByte: scope.EndByte(), ScopeStart: scope.StartByte()}}
	}
	return nil
}

// dynamicPythonIsNonePlaceholder reports whether n is the bare None literal,
// the common `self.x = None` placeholder used to declare a field before its
// real type is assigned elsewhere (typically later in __init__). Such
// placeholders must not shadow a real constructor assignment with an
// unknown-typed field binding.
func dynamicPythonIsNonePlaceholder(n *gotreesitter.Node, lang *gotreesitter.Language) bool {
	return n != nil && syntaxNodeType(n, lang) == "none"
}

// dynamicEnclosingFunction returns the nearest enclosing function_definition
// node, matching Python's function-level (not block-level) variable
// scoping. A trailing nested `def inner():` can end on the same byte as its
// enclosing function (Python's grammar has no closing token), so callers
// must key a variable's scope on this node's StartByte together with its
// EndByte, not EndByte alone.
func dynamicEnclosingFunction(n *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if syntaxNodeType(cur, lang) == "function_definition" {
			return cur
		}
	}
	return n
}

// dynamicPythonValueType resolves the syntactic type of a Python name
// introduction: an explicit annotation wins, otherwise a constructor-shaped
// call (Foo(...) or mod.Foo(...)) supplies the type, otherwise "".
func dynamicPythonValueType(right, typeField *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if typeField != nil {
		if text := dynamicPythonTypeText(dynamicPythonUnwrapTypeNode(typeField, lang), lang, src); text != "" {
			return text
		}
	}
	return dynamicPythonConstructedType(right, lang, src)
}

func dynamicPythonUnwrapTypeNode(n *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	if n != nil && syntaxNodeType(n, lang) == "type" && n.ChildCount() > 0 {
		return n.Child(0)
	}
	return n
}

// dynamicPythonTypeText resolves a Python type expression to a single class
// name: identifiers and dotted attributes pass through as written,
// Optional[Foo] and Foo | None collapse to Foo, and any other generic or
// multi-member union yields "" since it does not name one class.
func dynamicPythonTypeText(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil {
		return ""
	}
	switch syntaxNodeType(n, lang) {
	case "identifier", "attribute":
		return strings.TrimSpace(syntaxNodeText(n, src))
	case "generic_type":
		var base string
		var params []*gotreesitter.Node
		for i := 0; i < n.ChildCount(); i++ {
			child := n.Child(i)
			switch syntaxNodeType(child, lang) {
			case "identifier", "attribute":
				base = strings.TrimSpace(syntaxNodeText(child, src))
			case "type_parameter":
				for j := 0; j < child.ChildCount(); j++ {
					if syntaxNodeType(child.Child(j), lang) == "type" {
						params = append(params, child.Child(j))
					}
				}
			}
		}
		if base != "Optional" || len(params) != 1 {
			return ""
		}
		return dynamicPythonTypeText(dynamicPythonUnwrapTypeNode(params[0], lang), lang, src)
	case "binary_operator":
		if strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "operator"), src)) != "|" {
			return ""
		}
		left, right := syntaxField(n, lang, "left"), syntaxField(n, lang, "right")
		leftIsNone := syntaxNodeType(left, lang) == "none"
		rightIsNone := syntaxNodeType(right, lang) == "none"
		if leftIsNone == rightIsNone {
			return ""
		}
		if leftIsNone {
			return dynamicPythonTypeText(right, lang, src)
		}
		return dynamicPythonTypeText(left, lang, src)
	}
	return ""
}

// dynamicPythonConstructedType resolves `Foo(...)` / `mod.Foo(...)` call
// expressions to the constructed class name, requiring the last dotted
// segment of the callee to start with an uppercase letter.
func dynamicPythonConstructedType(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil || syntaxNodeType(n, lang) != "call" {
		return ""
	}
	name := dynamicSourceName(syntaxField(n, lang, "function"), lang, src)
	if name == "" {
		return ""
	}
	parts := strings.Split(name, ".")
	last := parts[len(parts)-1]
	if last == "" {
		return ""
	}
	if !unicode.IsUpper([]rune(last)[0]) {
		return ""
	}
	return name
}

func dynamicPythonParamBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	params := syntaxField(n, lang, "parameters")
	if params == nil {
		return nil
	}
	scopeStart, end := n.StartByte(), n.EndByte()
	var bindings []syntaxBinding
	for i := 0; i < params.ChildCount(); i++ {
		child := params.Child(i)
		if !child.IsNamed() {
			continue
		}
		bindings = append(bindings, dynamicPythonParamChildBindings(child, lang, src, scopeStart, end)...)
	}
	return bindings
}

func dynamicPythonParamChildBindings(child *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopeStart, end uint32) []syntaxBinding {
	switch syntaxNodeType(child, lang) {
	case "identifier":
		name := strings.TrimSpace(syntaxNodeText(child, src))
		if name == "" || name == "self" || name == "cls" {
			return nil
		}
		return []syntaxBinding{{Field: name, Local: true, StartByte: child.StartByte(), EndByte: end, ScopeStart: scopeStart}}
	case "typed_parameter":
		nameNode := firstDynamicChild(child, lang, "identifier")
		if nameNode == nil {
			return nil
		}
		name := strings.TrimSpace(syntaxNodeText(nameNode, src))
		if name == "" {
			return nil
		}
		typeText := dynamicPythonTypeText(dynamicPythonUnwrapTypeNode(syntaxField(child, lang, "type"), lang), lang, src)
		return []syntaxBinding{{Field: name, Type: typeText, Local: true, StartByte: child.StartByte(), EndByte: end, ScopeStart: scopeStart}}
	case "default_parameter":
		name := strings.TrimSpace(syntaxNodeText(syntaxField(child, lang, "name"), src))
		if name == "" {
			return nil
		}
		return []syntaxBinding{{Field: name, Local: true, StartByte: child.StartByte(), EndByte: end, ScopeStart: scopeStart}}
	case "typed_default_parameter":
		name := strings.TrimSpace(syntaxNodeText(syntaxField(child, lang, "name"), src))
		if name == "" {
			return nil
		}
		typeText := dynamicPythonTypeText(dynamicPythonUnwrapTypeNode(syntaxField(child, lang, "type"), lang), lang, src)
		return []syntaxBinding{{Field: name, Type: typeText, Local: true, StartByte: child.StartByte(), EndByte: end, ScopeStart: scopeStart}}
	case "list_splat_pattern", "dictionary_splat_pattern":
		nameNode := firstDynamicChild(child, lang, "identifier")
		if nameNode == nil {
			return nil
		}
		name := strings.TrimSpace(syntaxNodeText(nameNode, src))
		if name == "" {
			return nil
		}
		return []syntaxBinding{{Field: name, Local: true, StartByte: child.StartByte(), EndByte: end, ScopeStart: scopeStart}}
	}
	return nil
}

func dynamicPythonLambdaParamBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	params := syntaxField(n, lang, "parameters")
	if params == nil {
		return nil
	}
	scopeStart, end := n.StartByte(), n.EndByte()
	var bindings []syntaxBinding
	for i := 0; i < params.ChildCount(); i++ {
		child := params.Child(i)
		switch syntaxNodeType(child, lang) {
		case "identifier":
			name := strings.TrimSpace(syntaxNodeText(child, src))
			if name == "" {
				continue
			}
			bindings = append(bindings, syntaxBinding{Field: name, Local: true, StartByte: child.StartByte(), EndByte: end, ScopeStart: scopeStart})
		case "default_parameter":
			name := strings.TrimSpace(syntaxNodeText(syntaxField(child, lang, "name"), src))
			if name == "" {
				continue
			}
			bindings = append(bindings, syntaxBinding{Field: name, Local: true, StartByte: child.StartByte(), EndByte: end, ScopeStart: scopeStart})
		case "list_splat_pattern", "dictionary_splat_pattern":
			nameNode := firstDynamicChild(child, lang, "identifier")
			if nameNode == nil {
				continue
			}
			name := strings.TrimSpace(syntaxNodeText(nameNode, src))
			if name == "" {
				continue
			}
			bindings = append(bindings, syntaxBinding{Field: name, Local: true, StartByte: child.StartByte(), EndByte: end, ScopeStart: scopeStart})
		}
	}
	return bindings
}

func dynamicPythonForBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope) []syntaxBinding {
	if !dynamicHasFunctionScope(scopes) {
		return nil
	}
	left := syntaxField(n, lang, "left")
	if left == nil {
		return nil
	}
	scope := dynamicEnclosingFunction(n, lang)
	return dynamicPythonPatternBindings(left, lang, src, scope.StartByte(), scope.EndByte())
}

func dynamicPythonWithBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope) []syntaxBinding {
	if !dynamicHasFunctionScope(scopes) {
		return nil
	}
	value := syntaxField(n, lang, "value")
	if value == nil || syntaxNodeType(value, lang) != "as_pattern" {
		return nil
	}
	alias := syntaxField(value, lang, "alias")
	if alias == nil {
		return nil
	}
	scope := dynamicEnclosingFunction(n, lang)
	return dynamicPythonPatternBindings(alias, lang, src, scope.StartByte(), scope.EndByte())
}

func dynamicPythonExceptBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopes []dynamicScope) []syntaxBinding {
	if !dynamicHasFunctionScope(scopes) {
		return nil
	}
	value := syntaxField(n, lang, "value")
	if value == nil || syntaxNodeType(value, lang) != "as_pattern" {
		return nil
	}
	alias := syntaxField(value, lang, "alias")
	if alias == nil {
		return nil
	}
	scope := dynamicEnclosingFunction(n, lang)
	return dynamicPythonPatternBindings(alias, lang, src, scope.StartByte(), scope.EndByte())
}

func dynamicPythonComprehensionBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []syntaxBinding {
	scopeStart, end := n.StartByte(), n.EndByte()
	var bindings []syntaxBinding
	for i := 0; i < n.ChildCount(); i++ {
		child := n.Child(i)
		if syntaxNodeType(child, lang) != "for_in_clause" {
			continue
		}
		left := syntaxField(child, lang, "left")
		bindings = append(bindings, dynamicPythonPatternBindings(left, lang, src, scopeStart, end)...)
	}
	return bindings
}

// dynamicPythonPatternBindings flattens a Python assignment target (plain
// name or tuple/list unpacking) into Local bindings for every leaf name.
// scopeStart is the StartByte of the scope node whose EndByte is end.
func dynamicPythonPatternBindings(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, scopeStart, end uint32) []syntaxBinding {
	var bindings []syntaxBinding
	var walk func(*gotreesitter.Node)
	walk = func(cur *gotreesitter.Node) {
		if cur == nil {
			return
		}
		if syntaxNodeType(cur, lang) == "identifier" {
			if name := strings.TrimSpace(syntaxNodeText(cur, src)); name != "" {
				bindings = append(bindings, syntaxBinding{Field: name, Local: true, StartByte: cur.StartByte(), EndByte: end, ScopeStart: scopeStart})
			}
			return
		}
		for i := 0; i < cur.ChildCount(); i++ {
			walk(cur.Child(i))
		}
	}
	walk(n)
	return bindings
}
