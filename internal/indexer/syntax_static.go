package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
	return facts, ctx.Err()
}

func staticDeclaration(n *gotreesitter.Node, typ, container string, lang *gotreesitter.Language, src []byte, language string) (syntaxDeclaration, bool) {
	var kind string
	switch language {
	case "rust":
		switch typ {
		case "function_item":
			kind = graph.KindFunction
			if container != "" {
				kind = graph.KindMethod
			}
		case "struct_item", "enum_item", "trait_item", "type_item":
			kind = graph.KindType
		}
	case "java":
		switch typ {
		case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration", "annotation_type_declaration":
			kind = graph.KindType
		case "method_declaration", "constructor_declaration":
			kind = graph.KindMethod
		}
	case "kotlin":
		switch typ {
		case "class_declaration", "object_declaration", "type_alias":
			kind = graph.KindType
		case "function_declaration":
			kind = graph.KindFunction
			if container != "" {
				kind = graph.KindMethod
			}
		}
	case "c_sharp":
		switch typ {
		case "class_declaration", "struct_declaration", "interface_declaration", "enum_declaration", "record_declaration", "record_struct_declaration":
			kind = graph.KindType
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
			text := staticDottedName(object, lang, src) + "." + strings.TrimSpace(syntaxNodeText(name, src))
			if text != "." {
				return syntaxCall{Target: text, StartByte: object.StartByte()}, true
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
	target := staticDottedName(callee, lang, src)
	if target == "" {
		return syntaxCall{}, false
	}
	return syntaxCall{Target: target, StartByte: callee.StartByte()}, true
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
		if name == nil && syntaxNodeType(parent, lang) == "class_declaration" {
			name = staticIdentifierChild(parent, lang, "type_identifier", "simple_identifier")
		}
		return strings.TrimSpace(syntaxNodeText(name, src))
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
	case "impl_item", "trait_item", "struct_item", "enum_item", "class_declaration", "interface_declaration", "enum_declaration", "record_declaration", "annotation_type_declaration", "object_declaration", "struct_declaration", "record_struct_declaration":
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

func staticDottedName(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	if n == nil {
		return ""
	}
	typ := syntaxNodeType(n, lang)
	switch typ {
	case "field_expression", "scoped_identifier", "scoped_type_identifier", "navigation_expression", "qualified_name", "member_access_expression", "generic_name", "simple_identifier", "identifier", "type_identifier", "this", "super", "self":
		text := strings.TrimSpace(syntaxNodeText(n, src))
		text = strings.TrimSuffix(text, "()")
		return strings.ReplaceAll(text, " ", "")
	}
	for _, field := range []string{"field", "name", "scope", "object", "expression"} {
		if child := syntaxField(n, lang, field); child != nil {
			if text := staticDottedName(child, lang, src); text != "" {
				return text
			}
		}
	}
	text := strings.TrimSpace(syntaxNodeText(n, src))
	if text == "" {
		return ""
	}
	if i := strings.IndexAny(text, "(<"); i >= 0 {
		text = text[:i]
	}
	return strings.ReplaceAll(strings.TrimSpace(text), " ", "")
}

func staticLastSegment(s string) string {
	if i := strings.LastIndexAny(s, ".:"); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}
