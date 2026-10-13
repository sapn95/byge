package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"byge/internal/identity"
	"byge/internal/model"
)

// Stage 5 onboarding at the HTTP layer: the claim route, the onboarding block
// on the boards page, and the single-board redirect that must yield to an
// ownerless board. newEnv makes ownerEmail the bootstrap owner, so the default
// request caller may claim; other callers are set with e.user or their own
// request.

// makeOwnerlessBoard writes a board straight to the env's store with no grant,
// so it is genuinely ownerless. The bootstrap owner reaches it; nobody else
// does.
func makeOwnerlessBoard(t *testing.T, e *env, slug string) *model.Board {
	t.Helper()
	b := &model.Board{
		ID: model.NewID(), Slug: slug, Name: slug, Layout: model.LayoutColumns,
		SLA: model.DefaultSLA(), CreatedAt: today, UpdatedAt: today,
	}
	b.Columns = append(b.Columns, model.Column{ID: model.NewID(), BoardID: b.ID, Name: "To Do"})
	if err := e.st.CreateBoard(ownerCtx(), b); err != nil {
		t.Fatalf("CreateBoard(%q): %v", slug, err)
	}
	return b
}

// boardHasOwner reports whether the board carries any owner grant, read
// straight from the store.
func boardHasOwner(t *testing.T, e *env, boardID model.ID) bool {
	t.Helper()
	for _, g := range grantsOf(t, e, boardID) {
		if g.Role == model.RoleOwner {
			return true
		}
	}
	return false
}

// 9. POST /b/{board}/claim is actually requested: the bootstrap owner claims an
// ownerless board and it becomes owned. Plus two refusals that pin different
// layers.
func TestClaimRoute(t *testing.T) {
	t.Run("the bootstrap owner claims an ownerless board", func(t *testing.T) {
		e := newEnv(t, nil, nil)
		b := makeOwnerlessBoard(t, e, "free")

		rr := e.do(http.MethodPost, "/b/free/claim", nil)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/b/free" {
			t.Fatalf("claim = %d %q, want 303 to /b/free", rr.Code, rr.Header().Get("Location"))
		}
		if !boardHasOwner(t, e, b.ID) {
			t.Error("the board is still ownerless after a claim")
		}
	})

	// An arbitrary caller on the ownerless board is refused, and the refusal is
	// the Board RoleRead gate: on an ownerless board RoleOn returns "" for
	// mallory, so the board read fails before ClaimBoard is reached.
	t.Run("an arbitrary caller is refused at the board read", func(t *testing.T) {
		e := newEnv(t, nil, nil)
		b := makeOwnerlessBoard(t, e, "free")
		e.user = "mallory@example.com"
		if rr := e.do(http.MethodPost, "/b/free/claim", nil); rr.Code != http.StatusForbidden {
			t.Errorf("mallory claim = %d, want 403", rr.Code)
		}
		if boardHasOwner(t, e, b.ID) {
			t.Error("the board gained an owner from a refused claim")
		}
	})

	// A caller who can read an already-owned board but may not claim it is
	// refused by ClaimBoard's own guard. This is the case a dropped-guard
	// mutant turns green: it would write a second owner grant and 303.
	t.Run("a reader of an owned board is refused by ClaimBoard", func(t *testing.T) {
		e := seeded(t) // demo, owned by owner@example.com (the bootstrap owner)
		e.grant(t, e.board.ID, "mallory@example.com", model.RoleRead)
		e.user = "mallory@example.com"
		rr := e.do(http.MethodPost, "/b/demo/claim", nil)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("reader claim of an owned board = %d, want 403", rr.Code)
		}
		// Only the original owner, no second owner from the refused claim.
		owners := 0
		for _, g := range grantsOf(t, e, e.board.ID) {
			if g.Role == model.RoleOwner {
				owners++
			}
		}
		if owners != 1 {
			t.Errorf("owner grants = %d, want 1; the refused claim must write nothing", owners)
		}
	})

	t.Run("an unknown slug is a 404", func(t *testing.T) {
		e := newEnv(t, nil, nil)
		want(t, e.do(http.MethodPost, "/b/nope/claim", nil), http.StatusNotFound)
	})
}

// 10. The onboarding block renders for the bootstrap owner and not for an
// arbitrary caller.
func TestOnboardingBlockRenders(t *testing.T) {
	e := newEnv(t, nil, nil)
	makeOwnerlessBoard(t, e, "free")

	// The bootstrap owner sees the block and a claim action on GET /boards.
	rr := e.do(http.MethodGet, "/boards", nil)
	want(t, rr, http.StatusOK, "Boards waiting to be claimed", `action="/b/free/claim"`)

	// An arbitrary caller sees neither the block nor the board.
	e.user = "mallory@example.com"
	rr = e.do(http.MethodGet, "/boards", nil)
	if body := rr.Body.String(); strings.Contains(body, "Boards waiting to be claimed") || strings.Contains(body, "/b/free/claim") {
		t.Errorf("the onboarding block rendered for an arbitrary caller:\n%s", body)
	}
}

// 11. GET / does not swallow the onboarding block when the owner already holds
// one board, and the unchanged paths still behave.
func TestIndexYieldsToOwnerlessBoard(t *testing.T) {
	// The bootstrap owner holds exactly one owned board and an ownerless board
	// also exists: GET / renders the list with the block, no redirect.
	t.Run("one owned board plus an ownerless board renders the list", func(t *testing.T) {
		e := newEnv(t, nil, nil)
		if _, err := e.svc.CreateBoard(ownerCtx(), "Owned", "owned", nil); err != nil {
			t.Fatal(err)
		}
		makeOwnerlessBoard(t, e, "free")
		rr := e.do(http.MethodGet, "/", nil)
		want(t, rr, http.StatusOK, "Boards waiting to be claimed", `action="/b/free/claim"`, "/b/owned")
	})

	// The fresh-Pi owner holds zero boards and one ownerless board exists: the
	// list with the block, no redirect.
	t.Run("the fresh-Pi owner gets the list with the block", func(t *testing.T) {
		e := newEnv(t, nil, nil)
		makeOwnerlessBoard(t, e, "free")
		rr := e.do(http.MethodGet, "/", nil)
		want(t, rr, http.StatusOK, "Boards waiting to be claimed", `action="/b/free/claim"`)
	})

	// An ordinary caller with exactly one visible board and nothing to claim
	// still gets the single-board redirect. The caller is not the bootstrap
	// owner, so OwnerlessBoards is empty for them.
	t.Run("an ordinary one-board caller still redirects", func(t *testing.T) {
		e := newEnv(t, nil, nil)
		solo := identity.NewContext(context.Background(), identity.User{Email: "solo@example.com"})
		if _, err := e.svc.CreateBoard(solo, "Solo", "solo", nil); err != nil {
			t.Fatal(err)
		}
		// Even if an ownerless board exists, it is invisible to this caller.
		makeOwnerlessBoard(t, e, "free")
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(identity.NewContext(req.Context(), identity.User{Email: "solo@example.com"}))
		rr := httptest.NewRecorder()
		e.h.ServeHTTP(rr, req)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/b/solo" {
			t.Fatalf("ordinary one-board caller = %d %q, want 303 to /b/solo", rr.Code, rr.Header().Get("Location"))
		}
	})
}
