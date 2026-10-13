package service

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"
)

// Enforcement is a call every board-scoped method has to make, and a call
// every method has to make is the kind nobody notices is missing from the one
// added last. A hand-kept list of which methods are guarded goes stale the
// first time someone adds a method and forgets the list, which is the same
// failure the method itself has. So this does not keep a list of the guarded;
// it keeps the short list of the deliberately unguarded and reads the rest out
// of the source.
//
// It parses the two files that hold *Byge methods and fails for any exported
// method whose body does not call require( and is not in allowNoRequire. A new
// board-scoped method is therefore guarded or named here, and a method named
// here carries a one-line reason it is safe without the call. query.go and
// fuzzy.go hold no *Byge methods and are not read.
//
// Modelled on internal/web/api_doc_test.go: read the source, not the runtime,
// because the runtime cannot be asked which of its methods skipped a step, and
// hold it to an inverse allow-set so the predicate stays correct as the code
// grows.
func TestEveryBoardScopedMethodRequires(t *testing.T) {
	// Each entry is a method that is exported and on *Byge yet calls no
	// require(, with the reason it is safe. Exactly the enforcement design's
	// allow-set; anything else without a require( call is a bug this names.
	allowNoRequire := map[string]string{
		"Boards":             "lists every board; the filter is in VisibleBoards, it never refuses",
		"CreateBoard":        "the board does not exist yet; its guard is the identity check, and it writes the owner grant",
		"EnsureDefaultBoard": "bootstrap, runs before any identity and writes the default board directly",
		"Principals":         "an access primitive require is built from",
		"RoleOn":             "an access primitive require is built from",
		"VisibleBoards":      "an access primitive require is built from",
		"VisibleBoardList":   "the filtered board list; it drops boards the caller cannot read rather than refusing, built on VisibleBoards",
		"Groups":             "not board-scoped; guarded by requireGroupAdmin, a global check",
		"CreateGroup":        "not board-scoped; guarded by requireGroupAdmin",
		"UpdateGroup":        "not board-scoped; guarded by requireGroupAdmin",
		"DeleteGroup":        "not board-scoped; guarded by requireGroupAdmin",
	}

	fset := token.NewFileSet()
	var offenders []string
	seen := map[string]bool{}

	for _, name := range []string{"service.go", "access.go", "grants.go", "groups.go"} {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isBygeMethod(fn) || !ast.IsExported(fn.Name.Name) {
				continue
			}
			method := fn.Name.Name
			seen[method] = true

			var body strings.Builder
			if err := printer.Fprint(&body, fset, fn.Body); err != nil {
				t.Fatalf("print %s body: %v", method, err)
			}
			if strings.Contains(body.String(), "require(") {
				if reason, ok := allowNoRequire[method]; ok {
					// A method that both calls require( and is allow-listed is
					// a reason that has stopped being true: the list is meant
					// to explain the methods that do not call it.
					t.Errorf("%s calls require( yet is in allowNoRequire (%q); drop it from the list", method, reason)
				}
				continue
			}
			if _, ok := allowNoRequire[method]; !ok {
				offenders = append(offenders, method)
			}
		}
	}

	for _, method := range offenders {
		t.Errorf("%s is an exported *Byge method that calls no require( and is not in allowNoRequire", method)
	}

	// The allow-set must not outlive the methods it names: a reason kept for a
	// method that has been renamed or removed is a hole the next method of that
	// name falls straight through.
	for method := range allowNoRequire {
		if !seen[method] {
			t.Errorf("allowNoRequire names %s, which is not an exported *Byge method in service.go or access.go", method)
		}
	}
}

// isBygeMethod reports whether fn is a method on *Byge: it has a receiver, and
// that receiver's type is a *Byge (an *ast.StarExpr to the identifier Byge).
// A plain func, or a method on another type, is not scanned.
func isBygeMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Byge"
}
