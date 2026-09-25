package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type syntaxDeclaration struct {
	Name      string
	Kind      string
	Container string
	Detail    string
	StartByte uint32
	EndByte   uint32
}

type syntaxImport struct {
	Path     string
	Local    string
	Imported string
}

type syntaxCall struct {
	Target    string
	StartByte uint32
}

type syntaxFacts struct {
	Declarations []syntaxDeclaration
	Imports      []syntaxImport
	Calls        []syntaxCall
}

func parseSyntax(ctx context.Context, language, filename string, src []byte) (syntaxFacts, error) {
	if err := ctx.Err(); err != nil {
		return syntaxFacts{}, err
	}

	grammarName, err := syntaxGrammarName(language, filename)
	if err != nil {
		return syntaxFacts{}, err
	}
	entry := grammars.DetectLanguageByName(grammarName)
	if entry == nil || entry.Language == nil {
		return syntaxFacts{}, fmt.Errorf("syntax grammar %q is unavailable", grammarName)
	}
	lang := entry.Language()
	if lang == nil {
		return syntaxFacts{}, fmt.Errorf("syntax grammar %q failed to load", grammarName)
	}

	parser := gotreesitter.NewParser(lang)
	var cancelled uint32
	stop := context.AfterFunc(ctx, func() {
		atomic.StoreUint32(&cancelled, 1)
	})
	defer stop()
	parser.SetCancellationFlag(&cancelled)

	tree, err := parser.ParseStrict(src)
	if tree != nil {
		defer tree.Release()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return syntaxFacts{}, ctxErr
	}
	if err != nil {
		return syntaxFacts{}, fmt.Errorf("parse %s syntax: %w", filename, err)
	}
	if tree == nil || tree.RootNode() == nil {
		return syntaxFacts{}, fmt.Errorf("parse %s syntax: parser returned no tree", filename)
	}
	if tree.RootNode().HasError() {
		return syntaxFacts{}, fmt.Errorf("parse %s syntax: source contains syntax errors", filename)
	}

	if grammarName == "javascript" || grammarName == "typescript" || grammarName == "tsx" || grammarName == "python" {
		return extractDynamicSyntax(ctx, tree.RootNode(), lang, src, grammarName)
	}
	return extractStaticSyntax(ctx, tree.RootNode(), lang, src, grammarName)
}

func syntaxGrammarName(language, filename string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(language))
	if ext := strings.ToLower(filepath.Ext(filename)); ext == ".tsx" {
		return "tsx", nil
	}
	switch name {
	case "js", "javascript":
		return "javascript", nil
	case "ts", "typescript":
		return "typescript", nil
	case "tsx":
		return "tsx", nil
	case "py", "python":
		return "python", nil
	case "rs", "rust":
		return "rust", nil
	case "java":
		return "java", nil
	case "kt", "kotlin":
		return "kotlin", nil
	case "cs", "csharp", "c#":
		return "c_sharp", nil
	default:
		return "", fmt.Errorf("unsupported syntax language %q", language)
	}
}

func syntaxNodeText(node *gotreesitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	return node.Text(src)
}

func syntaxNodeType(node *gotreesitter.Node, lang *gotreesitter.Language) string {
	if node == nil || lang == nil {
		return ""
	}
	return node.Type(lang)
}

func syntaxField(node *gotreesitter.Node, lang *gotreesitter.Language, name string) *gotreesitter.Node {
	if node == nil || lang == nil {
		return nil
	}
	return node.ChildByFieldName(name, lang)
}

func walkSyntaxNamed(ctx context.Context, root *gotreesitter.Node, visit func(*gotreesitter.Node) bool) error {
	if root == nil {
		return nil
	}
	stack := []*gotreesitter.Node{root}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		i := len(stack) - 1
		node := stack[i]
		stack = stack[:i]
		if node.IsNamed() && !visit(node) {
			continue
		}
		for childIndex := node.ChildCount() - 1; childIndex >= 0; childIndex-- {
			stack = append(stack, node.Child(childIndex))
		}
	}
	return ctx.Err()
}
