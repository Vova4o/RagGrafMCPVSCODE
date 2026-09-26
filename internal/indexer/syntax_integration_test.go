package indexer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

// indexFixture writes files (repo-relative path -> source) under a fresh
// t.TempDir() and runs the real indexer.New().Index pipeline against it, so
// these tests exercise parsing, binding extraction, import resolution and
// call resolution together rather than any single stage in isolation.
func indexFixture(t *testing.T, files map[string]string) *graph.Graph {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		writeTestFile(t, filepath.Join(root, filepath.FromSlash(rel)), content)
	}
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	return value
}

// declGraphNode finds a declaration node disambiguated by its container, so
// fixtures with same-named declarations on different types (two "save"
// methods, or "index" declared on both a caller and its callee) resolve to
// the exact node instead of an arbitrary match.
func declGraphNode(t *testing.T, value *graph.Graph, file, container, name, kind string) graph.Node {
	t.Helper()
	suffix := name
	if container != "" {
		suffix = container + "." + name
	}
	want := "#" + suffix
	for _, node := range value.Nodes {
		if node.File == file && node.Kind == kind && node.Name == name && strings.HasSuffix(node.QualifiedName, want) {
			return node
		}
	}
	t.Fatalf("missing graph node file=%q container=%q name=%q kind=%q", file, container, name, kind)
	return graph.Node{}
}

func dumpCallEdgesFrom(value *graph.Graph, sourceID string) []string {
	var out []string
	for _, edge := range value.Edges {
		if edge.From == sourceID && edge.Kind == graph.EdgeCalls {
			target := graphNodeByID(value, edge.To)
			out = append(out, target.Kind+":"+target.QualifiedName)
		}
	}
	return out
}

func assertCallEdge(t *testing.T, value *graph.Graph, from, to graph.Node) {
	t.Helper()
	if !hasGraphEdge(value, from.ID, to.ID, graph.EdgeCalls) {
		t.Errorf("missing CALLS edge %s -> %s; edges from source = %v", from.QualifiedName, to.QualifiedName, dumpCallEdgesFrom(value, from.ID))
	}
}

func assertNoCallEdge(t *testing.T, value *graph.Graph, from, to graph.Node, reason string) {
	t.Helper()
	if hasGraphEdge(value, from.ID, to.ID, graph.EdgeCalls) {
		t.Errorf("%s: unexpected CALLS edge %s -> %s; edges from source = %v", reason, from.QualifiedName, to.QualifiedName, dumpCallEdgesFrom(value, from.ID))
	}
}

func assertSupertypeEdge(t *testing.T, value *graph.Graph, from, to graph.Node, kind string) {
	t.Helper()
	if !hasGraphEdge(value, from.ID, to.ID, kind) {
		t.Errorf("missing %s edge %s -> %s", kind, from.QualifiedName, to.QualifiedName)
	}
}

// TestIntegrationCrossLanguageCallResolution proves end to end, through the
// real indexer.New().Index pipeline on small fixture repos, that tree-sitter
// bindings/heritage feed the resolver correctly for: same-named methods
// disambiguated by receiver type, field-typed "this"/"self" chains, typed
// locals/params, unresolved/shadowed receivers, and inherited methods with
// EXTENDS/IMPLEMENTS edges.
func TestIntegrationCrossLanguageCallResolution(t *testing.T) {
	t.Parallel()

	t.Run("typescript", func(t *testing.T) {
		t.Parallel()
		value := indexFixture(t, map[string]string{
			"hierarchy.ts": `export interface Saveable {
  save(): void;
}

export class Base {
  helper(): void {}
}

export class Derived extends Base {
}
`,
			"stores.ts": `import { Saveable } from './hierarchy';

export class ReportStore implements Saveable {
  save(): void {}
}

export class AuditStore {
  save(): void {}
}
`,
			"service.ts": `import { ReportStore, AuditStore } from './stores';
import { Derived } from './hierarchy';

export class Indexer {
  index(): void {}
}

export class Service {
  indexer: Indexer;

  index(): void {
    this.indexer.index();
  }

  useTyped(x: ReportStore): void {
    x.save();
  }

  useUnknown(y): void {
    y.save();
  }

  shadowCase(): void {
    const x: ReportStore = new ReportStore();
    const cb = (x) => { x.save(); };
  }

  useDerived(d: Derived): void {
    d.helper();
  }
}
`,
		})
		assertNoSyntaxSkips(t, value)

		t.Run("direct_save_methods_declared", func(t *testing.T) {
			t.Parallel()
			declGraphNode(t, value, "stores.ts", "ReportStore", "save", graph.KindMethod)
			declGraphNode(t, value, "stores.ts", "AuditStore", "save", graph.KindMethod)
		})

		t.Run("same_name_field_call_no_self_edge", func(t *testing.T) {
			t.Parallel()
			serviceIndex := declGraphNode(t, value, "service.ts", "Service", "index", graph.KindMethod)
			indexerIndex := declGraphNode(t, value, "service.ts", "Indexer", "index", graph.KindMethod)
			assertCallEdge(t, value, serviceIndex, indexerIndex)
			assertNoCallEdge(t, value, serviceIndex, serviceIndex, "Service.index must not call itself")
		})

		t.Run("typed_param_resolves_to_report_store_only", func(t *testing.T) {
			t.Parallel()
			useTyped := declGraphNode(t, value, "service.ts", "Service", "useTyped", graph.KindMethod)
			reportSave := declGraphNode(t, value, "stores.ts", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "stores.ts", "AuditStore", "save", graph.KindMethod)
			assertCallEdge(t, value, useTyped, reportSave)
			assertNoCallEdge(t, value, useTyped, auditSave, "typed x: ReportStore must not resolve to AuditStore.save")
		})

		t.Run("untyped_receiver_resolves_to_neither_save", func(t *testing.T) {
			t.Parallel()
			useUnknown := declGraphNode(t, value, "service.ts", "Service", "useUnknown", graph.KindMethod)
			reportSave := declGraphNode(t, value, "stores.ts", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "stores.ts", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, useUnknown, reportSave, "untyped y must not resolve to ReportStore.save")
			assertNoCallEdge(t, value, useUnknown, auditSave, "untyped y must not resolve to AuditStore.save")
		})

		t.Run("lambda_param_shadow_does_not_leak_outer_typed_local", func(t *testing.T) {
			t.Parallel()
			shadowCase := declGraphNode(t, value, "service.ts", "Service", "shadowCase", graph.KindMethod)
			reportSave := declGraphNode(t, value, "stores.ts", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "stores.ts", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, shadowCase, reportSave, "lambda param x shadows outer x: ReportStore and must not resolve")
			assertNoCallEdge(t, value, shadowCase, auditSave, "lambda param x must not resolve to AuditStore.save either")
		})

		t.Run("inherited_method_and_heritage_edges", func(t *testing.T) {
			t.Parallel()
			useDerived := declGraphNode(t, value, "service.ts", "Service", "useDerived", graph.KindMethod)
			baseHelper := declGraphNode(t, value, "hierarchy.ts", "Base", "helper", graph.KindMethod)
			assertCallEdge(t, value, useDerived, baseHelper)

			derived := declGraphNode(t, value, "hierarchy.ts", "", "Derived", graph.KindType)
			base := declGraphNode(t, value, "hierarchy.ts", "", "Base", graph.KindType)
			assertSupertypeEdge(t, value, derived, base, graph.EdgeExtends)

			reportStore := declGraphNode(t, value, "stores.ts", "", "ReportStore", graph.KindType)
			saveable := declGraphNode(t, value, "hierarchy.ts", "", "Saveable", graph.KindType)
			assertSupertypeEdge(t, value, reportStore, saveable, graph.EdgeImplements)
		})
	})

	// JavaScript has no static types and no interfaces (implements/interface
	// are TS-only), so this fixture is a single file with constructor-inferred
	// local types instead of annotations, and it skips the IMPLEMENTS case
	// (already covered by the typescript subtest above).
	t.Run("javascript", func(t *testing.T) {
		t.Parallel()
		value := indexFixture(t, map[string]string{
			"app.js": `class Base {
  helper() {}
}

class Derived extends Base {
}

class Indexer {
  index() {}
}

class Service {
  index() {
    this.indexer = new Indexer();
    this.indexer.index();
  }

  useTyped() {
    const x = new ReportStore();
    x.save();
  }

  useUnknown(y) {
    y.save();
  }

  shadowCase() {
    const x = new ReportStore();
    const cb = (x) => { x.save(); };
  }

  useDerived() {
    const d = new Derived();
    d.helper();
  }
}

class ReportStore {
  save() {}
}

class AuditStore {
  save() {}
}
`,
		})
		assertNoSyntaxSkips(t, value)

		t.Run("same_name_field_call_no_self_edge", func(t *testing.T) {
			t.Parallel()
			serviceIndex := declGraphNode(t, value, "app.js", "Service", "index", graph.KindMethod)
			indexerIndex := declGraphNode(t, value, "app.js", "Indexer", "index", graph.KindMethod)
			assertCallEdge(t, value, serviceIndex, indexerIndex)
			assertNoCallEdge(t, value, serviceIndex, serviceIndex, "Service.index must not call itself")
		})

		t.Run("constructor_inferred_local_resolves_to_report_store_only", func(t *testing.T) {
			t.Parallel()
			useTyped := declGraphNode(t, value, "app.js", "Service", "useTyped", graph.KindMethod)
			reportSave := declGraphNode(t, value, "app.js", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "app.js", "AuditStore", "save", graph.KindMethod)
			assertCallEdge(t, value, useTyped, reportSave)
			assertNoCallEdge(t, value, useTyped, auditSave, "x = new ReportStore() must not resolve to AuditStore.save")
		})

		t.Run("untyped_receiver_resolves_to_neither_save", func(t *testing.T) {
			t.Parallel()
			useUnknown := declGraphNode(t, value, "app.js", "Service", "useUnknown", graph.KindMethod)
			reportSave := declGraphNode(t, value, "app.js", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "app.js", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, useUnknown, reportSave, "untyped y must not resolve to ReportStore.save")
			assertNoCallEdge(t, value, useUnknown, auditSave, "untyped y must not resolve to AuditStore.save")
		})

		t.Run("lambda_param_shadow_does_not_leak_outer_typed_local", func(t *testing.T) {
			t.Parallel()
			shadowCase := declGraphNode(t, value, "app.js", "Service", "shadowCase", graph.KindMethod)
			reportSave := declGraphNode(t, value, "app.js", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "app.js", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, shadowCase, reportSave, "lambda param x shadows outer x = new ReportStore() and must not resolve")
			assertNoCallEdge(t, value, shadowCase, auditSave, "lambda param x must not resolve to AuditStore.save either")
		})

		t.Run("inherited_method_and_extends_edge", func(t *testing.T) {
			t.Parallel()
			useDerived := declGraphNode(t, value, "app.js", "Service", "useDerived", graph.KindMethod)
			baseHelper := declGraphNode(t, value, "app.js", "Base", "helper", graph.KindMethod)
			assertCallEdge(t, value, useDerived, baseHelper)

			derived := declGraphNode(t, value, "app.js", "", "Derived", graph.KindType)
			base := declGraphNode(t, value, "app.js", "", "Base", graph.KindType)
			assertSupertypeEdge(t, value, derived, base, graph.EdgeExtends)
		})
	})

	t.Run("python", func(t *testing.T) {
		t.Parallel()
		value := indexFixture(t, map[string]string{
			"pkg/hierarchy.py": `from typing import Protocol


class Saveable(Protocol):
    def save(self) -> None:
        ...


class Base:
    def helper(self) -> None:
        pass


class Derived(Base):
    pass
`,
			"pkg/stores.py": `from .hierarchy import Saveable


class ReportStore(Saveable):
    def save(self) -> None:
        pass


class AuditStore:
    def save(self) -> None:
        pass
`,
			"pkg/service.py": `from .stores import ReportStore, AuditStore
from .hierarchy import Derived


class Indexer:
    def index(self) -> None:
        pass


class Service:
    indexer: Indexer

    def index(self) -> None:
        self.indexer.index()

    def use_typed(self, x: ReportStore) -> None:
        x.save()

    def use_unknown(self, y) -> None:
        y.save()

    def shadow_case(self) -> None:
        x = ReportStore()
        cb = lambda x: x.save()

    def use_derived(self, d: Derived) -> None:
        d.helper()
`,
		})
		assertNoSyntaxSkips(t, value)

		t.Run("direct_save_methods_declared", func(t *testing.T) {
			t.Parallel()
			declGraphNode(t, value, "pkg/stores.py", "ReportStore", "save", graph.KindMethod)
			declGraphNode(t, value, "pkg/stores.py", "AuditStore", "save", graph.KindMethod)
		})

		t.Run("same_name_field_call_no_self_edge", func(t *testing.T) {
			t.Parallel()
			serviceIndex := declGraphNode(t, value, "pkg/service.py", "Service", "index", graph.KindMethod)
			indexerIndex := declGraphNode(t, value, "pkg/service.py", "Indexer", "index", graph.KindMethod)
			assertCallEdge(t, value, serviceIndex, indexerIndex)
			assertNoCallEdge(t, value, serviceIndex, serviceIndex, "Service.index must not call itself")
		})

		t.Run("typed_param_resolves_to_report_store_only", func(t *testing.T) {
			t.Parallel()
			useTyped := declGraphNode(t, value, "pkg/service.py", "Service", "use_typed", graph.KindMethod)
			reportSave := declGraphNode(t, value, "pkg/stores.py", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "pkg/stores.py", "AuditStore", "save", graph.KindMethod)
			assertCallEdge(t, value, useTyped, reportSave)
			assertNoCallEdge(t, value, useTyped, auditSave, "typed x: ReportStore must not resolve to AuditStore.save")
		})

		t.Run("untyped_receiver_resolves_to_neither_save", func(t *testing.T) {
			t.Parallel()
			useUnknown := declGraphNode(t, value, "pkg/service.py", "Service", "use_unknown", graph.KindMethod)
			reportSave := declGraphNode(t, value, "pkg/stores.py", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "pkg/stores.py", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, useUnknown, reportSave, "untyped y must not resolve to ReportStore.save")
			assertNoCallEdge(t, value, useUnknown, auditSave, "untyped y must not resolve to AuditStore.save")
		})

		t.Run("lambda_param_shadow_does_not_leak_outer_typed_local", func(t *testing.T) {
			t.Parallel()
			shadowCase := declGraphNode(t, value, "pkg/service.py", "Service", "shadow_case", graph.KindMethod)
			reportSave := declGraphNode(t, value, "pkg/stores.py", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "pkg/stores.py", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, shadowCase, reportSave, "lambda param x shadows outer x = ReportStore() and must not resolve")
			assertNoCallEdge(t, value, shadowCase, auditSave, "lambda param x must not resolve to AuditStore.save either")
		})

		t.Run("inherited_method_and_heritage_edges", func(t *testing.T) {
			t.Parallel()
			useDerived := declGraphNode(t, value, "pkg/service.py", "Service", "use_derived", graph.KindMethod)
			baseHelper := declGraphNode(t, value, "pkg/hierarchy.py", "Base", "helper", graph.KindMethod)
			assertCallEdge(t, value, useDerived, baseHelper)

			derived := declGraphNode(t, value, "pkg/hierarchy.py", "", "Derived", graph.KindType)
			base := declGraphNode(t, value, "pkg/hierarchy.py", "", "Base", graph.KindType)
			assertSupertypeEdge(t, value, derived, base, graph.EdgeExtends)

			reportStore := declGraphNode(t, value, "pkg/stores.py", "", "ReportStore", graph.KindType)
			saveable := declGraphNode(t, value, "pkg/hierarchy.py", "", "Saveable", graph.KindType)
			assertSupertypeEdge(t, value, reportStore, saveable, graph.EdgeImplements)
		})
	})

	t.Run("java", func(t *testing.T) {
		t.Parallel()
		value := indexFixture(t, map[string]string{
			"com/example/hierarchy/Base.java": `package com.example.hierarchy;

public class Base {
  public void helper() {}
}
`,
			"com/example/hierarchy/Derived.java": `package com.example.hierarchy;

public class Derived extends Base {
}
`,
			"com/example/hierarchy/Saveable.java": `package com.example.hierarchy;

public interface Saveable {
  void save();
}
`,
			"com/example/stores/ReportStore.java": `package com.example.stores;

import com.example.hierarchy.Saveable;

public class ReportStore implements Saveable {
  public void save() {}
}
`,
			"com/example/stores/AuditStore.java": `package com.example.stores;

public class AuditStore {
  public void save() {}
}
`,
			"com/example/service/Indexer.java": `package com.example.service;

public class Indexer {
  public void index() {}
}
`,
			"com/example/service/Service.java": `package com.example.service;

import com.example.stores.ReportStore;
import com.example.hierarchy.Derived;

public class Service {
  private Indexer indexer;

  public void index() {
    this.indexer.index();
  }

  public void useTyped(ReportStore x) {
    x.save();
  }

  public void useUnknown(Object y) {
    y.save();
  }

  public void useDerived(Derived d) {
    d.helper();
  }
}
`,
			// Java forbids a lambda parameter from shadowing an outer local of the
			// same name in the same method (compile-time error), so the shadow
			// case is adapted to a sibling declaration whose own x:Object binding
			// must not resolve to a save() declared on an unrelated type.
			"com/example/service/Shadow.java": `package com.example.service;

public class Shadow {
  public void run(Object x) {
    x.save();
  }
}
`,
		})
		assertNoSyntaxSkips(t, value)

		t.Run("direct_save_methods_declared", func(t *testing.T) {
			t.Parallel()
			declGraphNode(t, value, "com/example/stores/ReportStore.java", "ReportStore", "save", graph.KindMethod)
			declGraphNode(t, value, "com/example/stores/AuditStore.java", "AuditStore", "save", graph.KindMethod)
		})

		t.Run("same_name_field_call_no_self_edge", func(t *testing.T) {
			t.Parallel()
			serviceIndex := declGraphNode(t, value, "com/example/service/Service.java", "Service", "index", graph.KindMethod)
			indexerIndex := declGraphNode(t, value, "com/example/service/Indexer.java", "Indexer", "index", graph.KindMethod)
			assertCallEdge(t, value, serviceIndex, indexerIndex)
			assertNoCallEdge(t, value, serviceIndex, serviceIndex, "Service.index must not call itself")
		})

		t.Run("typed_param_resolves_to_report_store_only", func(t *testing.T) {
			t.Parallel()
			useTyped := declGraphNode(t, value, "com/example/service/Service.java", "Service", "useTyped", graph.KindMethod)
			reportSave := declGraphNode(t, value, "com/example/stores/ReportStore.java", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "com/example/stores/AuditStore.java", "AuditStore", "save", graph.KindMethod)
			assertCallEdge(t, value, useTyped, reportSave)
			assertNoCallEdge(t, value, useTyped, auditSave, "typed x: ReportStore must not resolve to AuditStore.save")
		})

		t.Run("unresolved_type_receiver_resolves_to_neither_save", func(t *testing.T) {
			t.Parallel()
			useUnknown := declGraphNode(t, value, "com/example/service/Service.java", "Service", "useUnknown", graph.KindMethod)
			reportSave := declGraphNode(t, value, "com/example/stores/ReportStore.java", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "com/example/stores/AuditStore.java", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, useUnknown, reportSave, "y: Object must not resolve to ReportStore.save")
			assertNoCallEdge(t, value, useUnknown, auditSave, "y: Object must not resolve to AuditStore.save")
		})

		t.Run("sibling_scope_param_does_not_leak_typed_local", func(t *testing.T) {
			t.Parallel()
			shadowRun := declGraphNode(t, value, "com/example/service/Shadow.java", "Shadow", "run", graph.KindMethod)
			reportSave := declGraphNode(t, value, "com/example/stores/ReportStore.java", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "com/example/stores/AuditStore.java", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, shadowRun, reportSave, "Shadow.run's x: Object must not resolve to ReportStore.save")
			assertNoCallEdge(t, value, shadowRun, auditSave, "Shadow.run's x: Object must not resolve to AuditStore.save")
		})

		t.Run("inherited_method_and_heritage_edges", func(t *testing.T) {
			t.Parallel()
			useDerived := declGraphNode(t, value, "com/example/service/Service.java", "Service", "useDerived", graph.KindMethod)
			baseHelper := declGraphNode(t, value, "com/example/hierarchy/Base.java", "Base", "helper", graph.KindMethod)
			assertCallEdge(t, value, useDerived, baseHelper)

			derived := declGraphNode(t, value, "com/example/hierarchy/Derived.java", "", "Derived", graph.KindType)
			base := declGraphNode(t, value, "com/example/hierarchy/Base.java", "", "Base", graph.KindType)
			assertSupertypeEdge(t, value, derived, base, graph.EdgeExtends)

			reportStore := declGraphNode(t, value, "com/example/stores/ReportStore.java", "", "ReportStore", graph.KindType)
			saveable := declGraphNode(t, value, "com/example/hierarchy/Saveable.java", "", "Saveable", graph.KindType)
			assertSupertypeEdge(t, value, reportStore, saveable, graph.EdgeImplements)
		})
	})

	t.Run("kotlin", func(t *testing.T) {
		t.Parallel()
		value := indexFixture(t, map[string]string{
			"com/example/hierarchy/Base.kt": `package com.example.hierarchy

open class Base {
    fun helper() {}
}
`,
			"com/example/hierarchy/Derived.kt": `package com.example.hierarchy

class Derived : Base()
`,
			"com/example/hierarchy/Saveable.kt": `package com.example.hierarchy

interface Saveable {
    fun save()
}
`,
			"com/example/stores/ReportStore.kt": `package com.example.stores

import com.example.hierarchy.Saveable

class ReportStore : Saveable {
    override fun save() {}
}
`,
			"com/example/stores/AuditStore.kt": `package com.example.stores

class AuditStore {
    fun save() {}
}
`,
			"com/example/service/Indexer.kt": `package com.example.service

class Indexer {
    fun index() {}
}
`,
			"com/example/service/Service.kt": `package com.example.service

import com.example.stores.ReportStore
import com.example.hierarchy.Derived

class Service(val indexer: Indexer) {
    fun index() {
        this.indexer.index()
    }

    fun useTyped(x: ReportStore) {
        x.save()
    }

    fun useUnknown(y: Any) {
        y.save()
    }

    fun useDerived(d: Derived) {
        d.helper()
    }
}
`,
			// Like Java, Kotlin does not allow a lambda parameter to shadow an
			// outer local of the same name in the same scope, so the shadow case
			// uses a sibling declaration instead.
			"com/example/service/Shadow.kt": `package com.example.service

class Shadow {
    fun run(x: Any) {
        x.save()
    }
}
`,
		})
		assertNoSyntaxSkips(t, value)

		t.Run("direct_save_methods_declared", func(t *testing.T) {
			t.Parallel()
			declGraphNode(t, value, "com/example/stores/ReportStore.kt", "ReportStore", "save", graph.KindMethod)
			declGraphNode(t, value, "com/example/stores/AuditStore.kt", "AuditStore", "save", graph.KindMethod)
		})

		t.Run("same_name_field_call_no_self_edge", func(t *testing.T) {
			t.Parallel()
			serviceIndex := declGraphNode(t, value, "com/example/service/Service.kt", "Service", "index", graph.KindMethod)
			indexerIndex := declGraphNode(t, value, "com/example/service/Indexer.kt", "Indexer", "index", graph.KindMethod)
			assertCallEdge(t, value, serviceIndex, indexerIndex)
			assertNoCallEdge(t, value, serviceIndex, serviceIndex, "Service.index must not call itself")
		})

		t.Run("typed_param_resolves_to_report_store_only", func(t *testing.T) {
			t.Parallel()
			useTyped := declGraphNode(t, value, "com/example/service/Service.kt", "Service", "useTyped", graph.KindMethod)
			reportSave := declGraphNode(t, value, "com/example/stores/ReportStore.kt", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "com/example/stores/AuditStore.kt", "AuditStore", "save", graph.KindMethod)
			assertCallEdge(t, value, useTyped, reportSave)
			assertNoCallEdge(t, value, useTyped, auditSave, "typed x: ReportStore must not resolve to AuditStore.save")
		})

		t.Run("unresolved_type_receiver_resolves_to_neither_save", func(t *testing.T) {
			t.Parallel()
			useUnknown := declGraphNode(t, value, "com/example/service/Service.kt", "Service", "useUnknown", graph.KindMethod)
			reportSave := declGraphNode(t, value, "com/example/stores/ReportStore.kt", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "com/example/stores/AuditStore.kt", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, useUnknown, reportSave, "y: Any must not resolve to ReportStore.save")
			assertNoCallEdge(t, value, useUnknown, auditSave, "y: Any must not resolve to AuditStore.save")
		})

		t.Run("sibling_scope_param_does_not_leak_typed_local", func(t *testing.T) {
			t.Parallel()
			shadowRun := declGraphNode(t, value, "com/example/service/Shadow.kt", "Shadow", "run", graph.KindMethod)
			reportSave := declGraphNode(t, value, "com/example/stores/ReportStore.kt", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "com/example/stores/AuditStore.kt", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, shadowRun, reportSave, "Shadow.run's x: Any must not resolve to ReportStore.save")
			assertNoCallEdge(t, value, shadowRun, auditSave, "Shadow.run's x: Any must not resolve to AuditStore.save")
		})

		t.Run("inherited_method_and_heritage_edges", func(t *testing.T) {
			t.Parallel()
			useDerived := declGraphNode(t, value, "com/example/service/Service.kt", "Service", "useDerived", graph.KindMethod)
			baseHelper := declGraphNode(t, value, "com/example/hierarchy/Base.kt", "Base", "helper", graph.KindMethod)
			assertCallEdge(t, value, useDerived, baseHelper)

			derived := declGraphNode(t, value, "com/example/hierarchy/Derived.kt", "", "Derived", graph.KindType)
			base := declGraphNode(t, value, "com/example/hierarchy/Base.kt", "", "Base", graph.KindType)
			assertSupertypeEdge(t, value, derived, base, graph.EdgeExtends)

			reportStore := declGraphNode(t, value, "com/example/stores/ReportStore.kt", "", "ReportStore", graph.KindType)
			saveable := declGraphNode(t, value, "com/example/hierarchy/Saveable.kt", "", "Saveable", graph.KindType)
			assertSupertypeEdge(t, value, reportStore, saveable, graph.EdgeImplements)
		})
	})

	// C# resolves cross-file, unimported simple type names by same-directory
	// fallback in this indexer, so this fixture keeps all files flat in one
	// directory with no explicit `using` needed for the local types (mirrors
	// the brief's "C# same directory" case).
	t.Run("csharp", func(t *testing.T) {
		t.Parallel()
		value := indexFixture(t, map[string]string{
			"Hierarchy.cs": `public interface Saveable {
  void Save();
}

public class Base {
  public void Helper() {}
}

public class Derived : Base {
}
`,
			"Stores.cs": `public class ReportStore : Saveable {
  public void Save() {}
}

public class AuditStore {
  public void Save() {}
}
`,
			"Service.cs": `public class Indexer {
  public void Index() {}
}

public class Service {
  public Indexer Indexer { get; set; }

  public void Index() {
    this.Indexer.Index();
  }

  public void UseTyped(ReportStore x) {
    x.Save();
  }

  public void UseUnknown(object y) {
    y.Save();
  }

  public void UseDerived(Derived d) {
    d.Helper();
  }
}
`,
			// C# also treats reusing an outer local's name as a lambda parameter
			// as a compile error (CS0136), so the shadow case uses a sibling
			// declaration instead, same as Java/Kotlin above.
			"Shadow.cs": `public class Shadow {
  public void Run(object x) {
    x.Save();
  }
}
`,
		})
		assertNoSyntaxSkips(t, value)

		t.Run("direct_save_methods_declared", func(t *testing.T) {
			t.Parallel()
			declGraphNode(t, value, "Stores.cs", "ReportStore", "Save", graph.KindMethod)
			declGraphNode(t, value, "Stores.cs", "AuditStore", "Save", graph.KindMethod)
		})

		t.Run("same_name_field_call_no_self_edge", func(t *testing.T) {
			t.Parallel()
			serviceIndex := declGraphNode(t, value, "Service.cs", "Service", "Index", graph.KindMethod)
			indexerIndex := declGraphNode(t, value, "Service.cs", "Indexer", "Index", graph.KindMethod)
			assertCallEdge(t, value, serviceIndex, indexerIndex)
			assertNoCallEdge(t, value, serviceIndex, serviceIndex, "Service.Index must not call itself")
		})

		t.Run("typed_param_resolves_to_report_store_only", func(t *testing.T) {
			t.Parallel()
			useTyped := declGraphNode(t, value, "Service.cs", "Service", "UseTyped", graph.KindMethod)
			reportSave := declGraphNode(t, value, "Stores.cs", "ReportStore", "Save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "Stores.cs", "AuditStore", "Save", graph.KindMethod)
			assertCallEdge(t, value, useTyped, reportSave)
			assertNoCallEdge(t, value, useTyped, auditSave, "typed x: ReportStore must not resolve to AuditStore.Save")
		})

		t.Run("unresolved_type_receiver_resolves_to_neither_save", func(t *testing.T) {
			t.Parallel()
			useUnknown := declGraphNode(t, value, "Service.cs", "Service", "UseUnknown", graph.KindMethod)
			reportSave := declGraphNode(t, value, "Stores.cs", "ReportStore", "Save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "Stores.cs", "AuditStore", "Save", graph.KindMethod)
			assertNoCallEdge(t, value, useUnknown, reportSave, "y: object must not resolve to ReportStore.Save")
			assertNoCallEdge(t, value, useUnknown, auditSave, "y: object must not resolve to AuditStore.Save")
		})

		t.Run("sibling_scope_param_does_not_leak_typed_local", func(t *testing.T) {
			t.Parallel()
			shadowRun := declGraphNode(t, value, "Shadow.cs", "Shadow", "Run", graph.KindMethod)
			reportSave := declGraphNode(t, value, "Stores.cs", "ReportStore", "Save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "Stores.cs", "AuditStore", "Save", graph.KindMethod)
			assertNoCallEdge(t, value, shadowRun, reportSave, "Shadow.Run's x: object must not resolve to ReportStore.Save")
			assertNoCallEdge(t, value, shadowRun, auditSave, "Shadow.Run's x: object must not resolve to AuditStore.Save")
		})

		t.Run("inherited_method_and_heritage_edges", func(t *testing.T) {
			t.Parallel()
			useDerived := declGraphNode(t, value, "Service.cs", "Service", "UseDerived", graph.KindMethod)
			baseHelper := declGraphNode(t, value, "Hierarchy.cs", "Base", "Helper", graph.KindMethod)
			assertCallEdge(t, value, useDerived, baseHelper)

			derived := declGraphNode(t, value, "Hierarchy.cs", "", "Derived", graph.KindType)
			base := declGraphNode(t, value, "Hierarchy.cs", "", "Base", graph.KindType)
			assertSupertypeEdge(t, value, derived, base, graph.EdgeExtends)

			reportStore := declGraphNode(t, value, "Stores.cs", "", "ReportStore", graph.KindType)
			saveable := declGraphNode(t, value, "Hierarchy.cs", "", "Saveable", graph.KindType)
			assertSupertypeEdge(t, value, reportStore, saveable, graph.EdgeImplements)
		})
	})

	// Rust has no struct inheritance, so "extends" is modeled the way the
	// language actually expresses shared behaviour: a trait with a default
	// method (Base.helper) and `impl Base for Derived {}`, which this indexer
	// reports as an IMPLEMENTS edge and still resolves d.helper() through the
	// same hierarchy walk that backs EXTENDS for other languages. Cross-file
	// type references use `crate::` paths directly, since this indexer only
	// resolves Rust types that way (not through `use` aliases).
	t.Run("rust", func(t *testing.T) {
		t.Parallel()
		value := indexFixture(t, map[string]string{
			"src/stores.rs": `pub struct ReportStore;
impl ReportStore {
    pub fn save(&self) {}
}

pub struct AuditStore;
impl AuditStore {
    pub fn save(&self) {}
}
`,
			"src/hierarchy.rs": `pub trait Base {
    fn helper(&self) {}
}

pub struct Derived;
impl Base for Derived {}
`,
			"src/service.rs": `pub struct Indexer;
impl Indexer {
    pub fn index(&self) {}
}

pub struct Service {
    indexer: Indexer,
}

impl Service {
    pub fn index(&self) {
        self.indexer.index();
    }

    pub fn use_typed(&self, x: crate::stores::ReportStore) {
        x.save();
    }

    pub fn use_unknown(&self, y: i32) {
        y.save();
    }

    pub fn shadow_case(&self) {
        let x: crate::stores::ReportStore = crate::stores::ReportStore;
        let cb = |x: i32| { x.save(); };
    }

    pub fn use_derived(&self, d: crate::hierarchy::Derived) {
        d.helper();
    }
}
`,
		})
		assertNoSyntaxSkips(t, value)

		t.Run("direct_save_methods_declared", func(t *testing.T) {
			t.Parallel()
			declGraphNode(t, value, "src/stores.rs", "ReportStore", "save", graph.KindMethod)
			declGraphNode(t, value, "src/stores.rs", "AuditStore", "save", graph.KindMethod)
		})

		t.Run("same_name_field_call_no_self_edge", func(t *testing.T) {
			t.Parallel()
			serviceIndex := declGraphNode(t, value, "src/service.rs", "Service", "index", graph.KindMethod)
			indexerIndex := declGraphNode(t, value, "src/service.rs", "Indexer", "index", graph.KindMethod)
			assertCallEdge(t, value, serviceIndex, indexerIndex)
			assertNoCallEdge(t, value, serviceIndex, serviceIndex, "Service.index must not call itself")
		})

		t.Run("typed_param_resolves_to_report_store_only", func(t *testing.T) {
			t.Parallel()
			useTyped := declGraphNode(t, value, "src/service.rs", "Service", "use_typed", graph.KindMethod)
			reportSave := declGraphNode(t, value, "src/stores.rs", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "src/stores.rs", "AuditStore", "save", graph.KindMethod)
			assertCallEdge(t, value, useTyped, reportSave)
			assertNoCallEdge(t, value, useTyped, auditSave, "typed x: crate::stores::ReportStore must not resolve to AuditStore.save")
		})

		t.Run("primitive_typed_receiver_resolves_to_neither_save", func(t *testing.T) {
			t.Parallel()
			useUnknown := declGraphNode(t, value, "src/service.rs", "Service", "use_unknown", graph.KindMethod)
			reportSave := declGraphNode(t, value, "src/stores.rs", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "src/stores.rs", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, useUnknown, reportSave, "y: i32 must not resolve to ReportStore.save")
			assertNoCallEdge(t, value, useUnknown, auditSave, "y: i32 must not resolve to AuditStore.save")
		})

		t.Run("closure_param_shadow_does_not_leak_outer_typed_local", func(t *testing.T) {
			t.Parallel()
			shadowCase := declGraphNode(t, value, "src/service.rs", "Service", "shadow_case", graph.KindMethod)
			reportSave := declGraphNode(t, value, "src/stores.rs", "ReportStore", "save", graph.KindMethod)
			auditSave := declGraphNode(t, value, "src/stores.rs", "AuditStore", "save", graph.KindMethod)
			assertNoCallEdge(t, value, shadowCase, reportSave, "closure param x: i32 shadows outer x: ReportStore and must not resolve")
			assertNoCallEdge(t, value, shadowCase, auditSave, "closure param x: i32 must not resolve to AuditStore.save either")
		})

		t.Run("inherited_trait_default_method_and_implements_edge", func(t *testing.T) {
			t.Parallel()
			useDerived := declGraphNode(t, value, "src/service.rs", "Service", "use_derived", graph.KindMethod)
			baseHelper := declGraphNode(t, value, "src/hierarchy.rs", "Base", "helper", graph.KindMethod)
			assertCallEdge(t, value, useDerived, baseHelper)

			derived := declGraphNode(t, value, "src/hierarchy.rs", "", "Derived", graph.KindType)
			base := declGraphNode(t, value, "src/hierarchy.rs", "", "Base", graph.KindType)
			assertSupertypeEdge(t, value, derived, base, graph.EdgeImplements)
		})
	})

	t.Run("go", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.24\n")
		writeTestFile(t, filepath.Join(root, "main.go"), `package m

type Dep struct{}

func (d *Dep) Index() {}

type Service struct {
	dep *Dep
}

func (s *Service) Index() {
	s.dep.Index()
}
`)
		value := mustIndexGo(t, root)
		if !hasTypedCallEdge(t, value, "example.com/m.Service.Index", "example.com/m.Dep.Index") {
			t.Fatalf("missing typed edge Service.Index -> Dep.Index; edges=%#v", value.Edges)
		}
		if hasCallEdge(value, "example.com/m.Service.Index", "example.com/m.Service.Index") {
			t.Fatalf("unexpected self-edge on Service.Index")
		}
	})
}
