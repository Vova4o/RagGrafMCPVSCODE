package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
	var visit func(*gotreesitter.Node, []dynamicScope) error
	visit = func(node *gotreesitter.Node, scopes []dynamicScope) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		typ := syntaxNodeType(node, lang)
		name := dynamicDeclarationName(node, lang, src, typ)
		kind := dynamicDeclarationKind(node, lang, typ, language, len(scopes) > 0 && scopes[len(scopes)-1].kind == "class")
		if kind != "" && name != "" {
			container := dynamicContainer(scopes)
			detail := syntaxHeader(node, lang, src, typ)
			facts.Declarations = append(facts.Declarations, syntaxDeclaration{
				Name: name, Kind: kind, Container: container, Detail: detail,
				StartByte: node.StartByte(), EndByte: node.EndByte(),
			})
			scopes = append(scopes, dynamicScope{name: name, kind: dynamicScopeKind(typ, kind)})
		}
		if language == "python" {
			if err := dynamicPythonImports(node, lang, src, &facts); err != nil {
				return err
			}
		} else {
			if err := dynamicJSImports(node, lang, src, &facts); err != nil {
				return err
			}
		}
		if target := dynamicCallTarget(node, lang, src, typ, language); target != "" {
			facts.Calls = append(facts.Calls, syntaxCall{Target: target, StartByte: node.StartByte()})
		}
		for i := 0; i < node.ChildCount(); i++ {
			if err := visit(node.Child(i), scopes); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, nil); err != nil {
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
	case "function_declaration", "function_definition", "class_declaration", "class_definition", "interface_declaration", "type_alias_declaration", "enum_declaration", "method_definition":
		return strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "name"), src))
	case "variable_declarator":
		value := syntaxField(n, lang, "value")
		if value == nil {
			value = syntaxField(n, lang, "right")
		}
		if value != nil && isDynamicFunction(syntaxNodeType(value, lang)) {
			return strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "name"), src))
		}
	case "assignment":
		value := syntaxField(n, lang, "right")
		if value != nil && isDynamicFunction(syntaxNodeType(value, lang)) {
			return strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "left"), src))
		}
	}
	return ""
}

func dynamicDeclarationKind(n *gotreesitter.Node, lang *gotreesitter.Language, typ, language string, inClass bool) string {
	switch typ {
	case "class_declaration", "class_definition", "interface_declaration", "type_alias_declaration", "enum_declaration":
		return graph.KindType
	case "function_declaration", "function_definition":
		if inClass {
			return graph.KindMethod
		}
		return graph.KindFunction
	case "method_definition":
		return graph.KindMethod
	case "variable_declarator", "assignment":
		value := syntaxField(n, lang, "value")
		if value == nil {
			value = syntaxField(n, lang, "right")
		}
		if value != nil && isDynamicFunction(syntaxNodeType(value, lang)) {
			if inClass {
				return graph.KindMethod
			}
			return graph.KindFunction
		}
	}
	return ""
}

func isDynamicFunction(typ string) bool {
	return typ == "arrow_function" || typ == "function_expression" || typ == "lambda"
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
	if typ == "variable_declarator" {
		init := syntaxField(n, lang, "value")
		if init != nil && syntaxNodeType(init, lang) == "call_expression" {
			fn := syntaxField(init, lang, "function")
			args := syntaxField(init, lang, "arguments")
			if strings.TrimSpace(syntaxNodeText(fn, src)) == "require" && args != nil && args.ChildCount() > 0 {
				path := dynamicString(args.Child(0), lang, src)
				local := strings.TrimSpace(syntaxNodeText(syntaxField(n, lang, "name"), src))
				if path != "" && local != "" {
					facts.Imports = append(facts.Imports, syntaxImport{Path: path, Local: local, Imported: ""})
				}
			}
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
