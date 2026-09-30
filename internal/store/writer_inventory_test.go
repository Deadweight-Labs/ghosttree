package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var writerMethodClasses = map[string]string{
	"AddCriterion":                  "write",
	"AddEvidence":                   "write",
	"AddPerson":                     "write",
	"AddRequestRelation":            "write",
	"AppendChunkBatches":            "write",
	"AppendChunks":                  "write",
	"ApplyRequestDistillation":      "write",
	"ApplySessionDistillation":      "write",
	"ApplyStaleness":                "write",
	"ArchiveGhostFiles":             "write",
	"Authenticate":                  "read",
	"AuthenticatePrincipal":         "read",
	"BackfillObservedAt":            "write",
	"Backup":                        "administrative",
	"BeginMigration":                "write",
	"BilledTranscriptChars":         "read",
	"BranchBoundKnowledge":          "read",
	"CanonicalizeScopes":            "write",
	"Close":                         "administrative",
	"CloseDistillBatch":             "write",
	"CompleteMigration":             "write",
	"CompleteRequest":               "write",
	"CompletedDocumentArtifacts":    "read",
	"CompletedMigrationArtifacts":   "read",
	"ContextSnapshot":               "read",
	"ContextSnapshotAccess":         "read",
	"ContextSnapshotEntries":        "read",
	"CountOpenRequests":             "read",
	"CountPendingWithoutProject":    "read",
	"CreateContextSnapshot":         "write",
	"CreateDocument":                "write",
	"CreateRequest":                 "write",
	"DB":                            "administrative",
	"DistillBatchItems":             "read",
	"DistillBatchUsage":             "read",
	"DistillCost":                   "read",
	"DocumentByID":                  "read",
	"DocumentBySlug":                "read",
	"DocumentRevision":              "read",
	"DocumentRevisions":             "read",
	"Documents":                     "read",
	"DropRequest":                   "write",
	"EvidenceFor":                   "read",
	"FinishRequestWork":             "write",
	"GhostFileByPath":               "read",
	"GhostFileChain":                "read",
	"GhostFileHistory":              "read",
	"GhostFilesForDelivery":         "write",
	"GhostFilesUnder":               "read",
	"GhostHistoryCount":             "read",
	"GhostReviewsUnder":             "read",
	"ImportDocument":                "write",
	"InsertDocumentMigration":       "write",
	"InsertKnowledge":               "write",
	"InsertMigrated":                "write",
	"InterruptedWork":               "read",
	"KnowledgeByID":                 "read",
	"KnowledgeForActivatedContext":  "read_with_best_effort",
	"KnowledgeForActivatedPreview":  "read",
	"KnowledgeForContext":           "read_with_best_effort",
	"KnowledgeForProject":           "read",
	"KnowledgeHistory":              "read",
	"KnowledgeTitlesForPrompt":      "read",
	"KnowledgeUnusedSince":          "read",
	"KnowledgeUsage":                "read",
	"ListContextSnapshots":          "read",
	"ListSessions":                  "read",
	"MigrationEvidenceForKnowledge": "read",
	"MoveGhostFile":                 "write",
	"OpenDistillBatches":            "read",
	"OrphanGhostFiles":              "read",
	"PatchDocument":                 "write",
	"PendingDistillationSize":       "read",
	"PendingKnowledge":              "read",
	"PrepareGhostArchive":           "read",
	"PreviewCanonicalizeScopes":     "write",
	"PrincipalByName":               "read",
	"ProbeRelevance":                "read",
	"PushRevision":                  "write",
	"PutGhostFile":                  "write",
	"PutGhostReview":                "write",
	"ReadSession":                   "read",
	"RecordDistillBatch":            "write",
	"RecordDistillBatchUsage":       "write",
	"Recurrence":                    "read",
	"RegressionGaps":                "read",
	"ReleaseDistillations":          "write",
	"RelevantKnowledge":             "read_with_best_effort",
	"RemoveRequestRelation":         "write",
	"RequestByID":                   "read",
	"RequestQuotes":                 "read",
	"RequestSightings":              "read",
	"RequestTitlesForPrompt":        "read",
	"RuntimeStats":                  "administrative",
	"SearchAllKnowledge":            "read",
	"SearchGhostFiles":              "read",
	"SearchKnowledge":               "read_with_best_effort",
	"SearchKnowledgeForContext":     "read_with_best_effort",
	"SearchRequestSessions":         "read",
	"SearchRequests":                "read",
	"SearchSessions":                "read",
	"SessionByID":                   "read",
	"SessionDistillationExists":     "read",
	"SessionRaw":                    "read",
	"SessionsPendingDistillation":   "read",
	"SetActivation":                 "write",
	"SetContextSnapshotAccess":      "write",
	"SetCriterionState":             "write",
	"SetRegressionCover":            "write",
	"StartRequestWork":              "write",
	"ToolCallsPerProject":           "read",
	"TouchMachine":                  "best_effort",
	"UnbindBranchScope":             "write",
	"UpdateKnowledge":               "write",
	"UpdateKnowledgeBy":             "write",
	"UpdateRequest":                 "write",
	"UpsertSession":                 "write",
}

type writerSource struct {
	methods   map[string]*ast.FuncDecl
	functions map[string]*ast.FuncDecl
}

func parseWriterSource(t *testing.T, paths map[string]string) writerSource {
	t.Helper()
	out := writerSource{map[string]*ast.FuncDecl{}, map[string]*ast.FuncDecl{}}
	for path, source := range paths {
		var input any
		if source != "" {
			input = source
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, input, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if fn.Recv == nil {
				out.functions[fn.Name.Name] = fn
				continue
			}
			recv := fn.Recv.List[0].Type
			if ptr, ok := recv.(*ast.StarExpr); ok {
				recv = ptr.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == "Store" {
				out.methods[fn.Name.Name] = fn
			}
		}
	}
	return out
}

func TestWriterInventoryRejectsBypasses(t *testing.T) {
	cases := []struct{ name, source, class string }{
		{"new_public_method", `func(s *Store) Surprise() { s.db.Exec("DELETE FROM sessions") }`, ""},
		{"write_inside_route", `func(s *Store) Write() { if s.writer != nil { s.db.Exec("DELETE FROM sessions"); return queueWrite(s,nil,nil) } }`, "write"},
		{"hidden_prelude", `func(s *Store) Write() { hidden(s); if s.writer != nil { return queueWrite(s,nil,nil) } }; func hidden(s *Store) { s.db.Exec("DELETE FROM sessions") }`, "write"},
		{"missing_queue", `func(s *Store) Write() { s.db.Exec("DELETE FROM sessions") }`, "write"},
		{"mutation_before_queue", `func(s *Store) Write() { s.db.Exec("DELETE FROM sessions"); if s.writer != nil { return queueWrite(s,nil,nil) } }`, "write"},
		{"fallthrough", `func(s *Store) Write() { if s.writer != nil { queueWrite(s,nil,nil) }; s.db.Exec("DELETE FROM sessions") }`, "write"},
		{"missing_reader", `func(s *Store) Read() { s.db.Query("SELECT 1") }`, "read"},
		{"read_reaches_write", `func(s *Store) Read() { if s.reader != nil { return s.reader.Read() }; return s.mutate() }; func(s *Store) mutate() { s.db.Exec("DELETE FROM sessions") }`, "read"},
		{"query_returning_write", `func(s *Store) Read() { if s.reader != nil { return s.reader.Read() }; return s.db.QueryRow("INSERT INTO sessions VALUES(1) RETURNING id") }`, "read"},
		{"free_helper_write", `func(s *Store) Read() { if s.reader != nil { return s.reader.Read() }; return hidden(s.db) }; func hidden(db *sql.DB) { db.Exec("DELETE FROM sessions") }`, "read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classes := map[string]string{}
			if tc.class != "" {
				if tc.class == "write" {
					classes["Write"] = tc.class
				} else {
					classes["Read"] = tc.class
				}
			}
			src := parseWriterSource(t, map[string]string{"fixture.go": "package store;" + tc.source})
			if got := inspectWriterInventory(src, classes); len(got) == 0 {
				t.Fatal("unsafe runtime entry was accepted")
			}
		})
	}
}

func TestRuntimeStoreWriterInventory(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, path := range paths {
		if !strings.HasSuffix(path, "_test.go") {
			sources[path] = ""
		}
	}
	for _, issue := range inspectWriterInventory(parseWriterSource(t, sources), writerMethodClasses) {
		t.Error(issue)
	}
}

func inspectWriterInventory(src writerSource, classes map[string]string) []string {
	var issues []string
	for name, fn := range src.methods {
		if !ast.IsExported(name) {
			continue
		}
		class, ok := classes[name]
		if !ok {
			issues = append(issues, name+": unclassified public Store method")
			continue
		}
		field := ""
		switch class {
		case "write":
			field = "writer"
		case "read", "read_with_best_effort":
			field = "reader"
		case "best_effort":
			field = "bookkeeper"
		case "administrative":
			continue
		default:
			issues = append(issues, name+": unknown classification")
			continue
		}
		if !hasRuntimeRoute(fn, field) {
			issues = append(issues, name+": missing early terminating "+field+" route")
		}
		if field == "reader" {
			if bad := unsafeRead(src, fn, map[*ast.FuncDecl]bool{}); bad != "" {
				issues = append(issues, name+": read reaches "+bad)
			}
		}
	}
	for name := range classes {
		if src.methods[name] == nil {
			issues = append(issues, name+": stale inventory entry")
		}
	}
	for _, name := range []string{"bumpUsage", "TouchMachine"} {
		if fn := src.methods[name]; fn != nil && !hasRuntimeRoute(fn, "bookkeeper") {
			issues = append(issues, name+": best-effort bypass")
		}
	}
	return issues
}

func storeReceiver(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

func isField(expr ast.Expr, receiver, field string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == receiver && sel.Sel.Name == field
}

func hasRuntimeRoute(fn *ast.FuncDecl, field string) bool {
	receiver := storeReceiver(fn)
	for _, stmt := range fn.Body.List {
		if branch, ok := stmt.(*ast.IfStmt); ok && branch.Init == nil && branch.Else == nil {
			cond, ok := branch.Cond.(*ast.BinaryExpr)
			if ok && cond.Op == token.NEQ && isField(cond.X, receiver, field) {
				nilID, ok := cond.Y.(*ast.Ident)
				if !ok || nilID.Name != "nil" || len(branch.Body.List) == 0 {
					return false
				}
				if _, ok := branch.Body.List[len(branch.Body.List)-1].(*ast.ReturnStmt); !ok {
					return false
				}
				routed := false
				ast.Inspect(branch.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if id, ok := call.Fun.(*ast.Ident); ok && field == "writer" {
						routed = routed || id.Name == "queueValue" || id.Name == "queueWrite" || id.Name == "queueContextValue"
					}
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && isField(sel.X, receiver, field) {
						switch field {
						case "reader":
							routed = routed || sel.Sel.Name == fn.Name.Name
						case "writer":
							routed = routed || sel.Sel.Name == "admitChunks"
						case "bookkeeper":
							routed = routed || sel.Sel.Name == "coalesceUsage" || sel.Sel.Name == "coalesceMachine"
						}
					}
					return true
				})
				return routed && runtimeBranchUsesOnlyDispatch(branch.Body, receiver, field)
			}
		}
		used := usesStoreReceiver(stmt, receiver)
		if used {
			return false
		}
	}
	return false
}

func usesStoreReceiver(node ast.Node, receiver string) bool {
	used := false
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == receiver {
			used = true
		}
		return !used
	})
	return used
}

func runtimeBranchUsesOnlyDispatch(node ast.Node, receiver, field string) bool {
	safe := true
	ast.Inspect(node, func(n ast.Node) bool {
		if !safe {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			exempt := -1
			approved := false
			if target, ok := call.Fun.(*ast.Ident); ok && field == "writer" {
				switch target.Name {
				case "queueWrite", "queueValue":
					exempt, approved = 0, true
				case "queueContextValue":
					exempt, approved = 1, true
				}
			}
			if target, ok := call.Fun.(*ast.SelectorExpr); ok && isField(target.X, receiver, field) {
				approved = (field == "reader") || (field == "writer" && (target.Sel.Name == "admitChunks" || target.Sel.Name == "reject")) || (field == "bookkeeper" && (target.Sel.Name == "coalesceUsage" || target.Sel.Name == "coalesceMachine"))
			}
			if approved {
				for i, arg := range call.Args {
					if i == exempt {
						id, ok := arg.(*ast.Ident)
						safe = safe && ok && id.Name == receiver
					} else {
						safe = safe && !usesStoreReceiver(arg, receiver)
					}
				}
				return false
			}
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == receiver {
			safe = false
		}
		return safe
	})
	return safe
}

func unsafeRead(src writerSource, fn *ast.FuncDecl, seen map[*ast.FuncDecl]bool) string {
	if seen[fn] {
		return ""
	}
	seen[fn] = true
	receiver := storeReceiver(fn)
	var bad string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if bad != "" {
			return false
		}
		if literal, ok := n.(*ast.BasicLit); ok && literal.Kind == token.STRING {
			value, _ := strconv.Unquote(literal.Value)
			sql := strings.ToUpper(strings.TrimSpace(value))
			for _, prefix := range []string{"INSERT INTO ", "INSERT OR ", "UPDATE ", "DELETE FROM ", "REPLACE INTO ", "CREATE TABLE ", "ALTER TABLE "} {
				if strings.HasPrefix(sql, prefix) {
					bad = fn.Name.Name + " mutating SQL"
					return false
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var next *ast.FuncDecl
		switch target := call.Fun.(type) {
		case *ast.Ident:
			next = src.functions[target.Name]
		case *ast.SelectorExpr:
			if target.Sel.Name == "Exec" || target.Sel.Name == "ExecContext" {
				bad = fn.Name.Name + "." + target.Sel.Name
				return false
			}
			if id, ok := target.X.(*ast.Ident); ok && receiver != "" && id.Name == receiver {
				if target.Sel.Name == "recordKnowledgeUse" || target.Sel.Name == "recordKnowledgeSearchHit" || target.Sel.Name == "bumpUsage" {
					return true
				}
				if class := writerMethodClasses[target.Sel.Name]; class == "write" || class == "administrative" || class == "best_effort" {
					bad = target.Sel.Name
					return false
				}
				next = src.methods[target.Sel.Name]
			}
		}
		if next != nil {
			bad = unsafeRead(src, next, seen)
		}
		return bad == ""
	})
	return bad
}

func TestRuntimeDBEscapeGuard(t *testing.T) {
	allowed := map[string]map[string]int{
		"cmd/ctx/serve.go":                      {"runServer": 3},
		"internal/storebench/sqlite_current.go": {"executeOn": 1, "sqliteSettings": 1, "sqliteVersion": 1},
		"internal/storebench/sqlite_queued.go":  {"Stats": 1},
		"internal/storebench/verify.go":         {"verifySQLite": 1, "migrationArtifacts": 1, "verifyTableCounts": 1, "verifySQLiteIntegrity": 2},
	}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && path != root {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			owner := "package"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "DB" {
					return true
				}
				if allowed[relative][owner] <= 0 {
					t.Errorf("unreviewed DB escape at %s (%s)", fset.Position(sel.Pos()), owner)
				} else {
					allowed[relative][owner]--
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, functions := range allowed {
		for name, remaining := range functions {
			if remaining != 0 {
				t.Error(fmt.Sprintf("stale DB escape allowlist: %s %s: %d", path, name, remaining))
			}
		}
	}
}
