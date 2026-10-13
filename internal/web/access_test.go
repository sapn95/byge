package web

import (
	"net/http"
	"testing"

	"byge/internal/model"
)

// The board access page and its two writes are owner-only, enforced in the
// service. Every row of the design's grants matrix is a sub-test here, and
// every one of the three routes is requested, so none is a route no test runs.

// grantsOf reads a board's grants straight from the store, to assert what a
// write did without going through the owner-only service method.
func grantsOf(t *testing.T, e *env, board model.ID) []model.Grant {
	t.Helper()
	g, err := e.st.ListGrants(ownerCtx(), board)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestAccessPageIsOwnerOnly(t *testing.T) {
	e := seeded(t)
	e.grant(t, e.board.ID, "writer@example.com", model.RoleWrite)
	e.grant(t, e.board.ID, "reader@example.com", model.RoleRead)

	// The owner sees the page and the one owner grant on it.
	want(t, e.do(http.MethodGet, "/b/demo/access", nil), http.StatusOK, "owner@example.com", "owner")

	// Everyone else is refused, including an unidentified caller.
	for _, tc := range []struct {
		name, user string
	}{
		{"writer", "writer@example.com"},
		{"reader", "reader@example.com"},
		{"stranger", "stranger@example.com"},
		{"anonymous", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.user = tc.user
			if rr := e.do(http.MethodGet, "/b/demo/access", nil); rr.Code != http.StatusForbidden {
				t.Errorf("GET /b/demo/access as %s = %d, want 403", tc.name, rr.Code)
			}
		})
	}
}

func TestSetGrant(t *testing.T) {
	t.Run("owner adds a user grant", func(t *testing.T) {
		e := seeded(t)
		rr := e.do(http.MethodPost, "/b/demo/access", form("kind", "user", "id", "ada@example.com", "role", "read"))
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/b/demo/access" {
			t.Fatalf("status = %d, Location = %q", rr.Code, rr.Header().Get("Location"))
		}
		found := false
		for _, g := range grantsOf(t, e, e.board.ID) {
			if g.Principal == model.UserPrincipal("ada@example.com") && g.Role == model.RoleRead {
				found = true
			}
		}
		if !found {
			t.Error("the grant was not written")
		}
	})

	t.Run("owner adds a local group grant", func(t *testing.T) {
		e := seeded(t)
		g, err := e.svc.CreateGroup(ownerCtx(), "Platform", []string{"ada@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		rr := e.do(http.MethodPost, "/b/demo/access", form("kind", "group", "id", string(g.ID), "role", "write"))
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		found := false
		for _, gr := range grantsOf(t, e, e.board.ID) {
			if gr.Principal == model.GroupPrincipal(g.ID) {
				found = true
			}
		}
		if !found {
			t.Error("the group grant was not written")
		}
	})

	t.Run("a group id that does not exist is a 400 and writes nothing", func(t *testing.T) {
		e := seeded(t)
		before := len(grantsOf(t, e, e.board.ID))
		want(t, e.do(http.MethodPost, "/b/demo/access", form("kind", "group", "id", "nope", "role", "read")),
			http.StatusBadRequest, "no such local group")
		if len(grantsOf(t, e, e.board.ID)) != before {
			t.Error("a grant was written for a group that does not exist")
		}
	})

	t.Run("a bad role is a 400", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/b/demo/access", form("kind", "user", "id", "ada@example.com", "role", "admin")),
			http.StatusBadRequest)
	})

	t.Run("an empty id is a 400", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/b/demo/access", form("kind", "user", "id", "", "role", "read")),
			http.StatusBadRequest)
	})

	t.Run("an unknown kind is a 400", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/b/demo/access", form("kind", "wizard", "id", "x", "role", "read")),
			http.StatusBadRequest, "unknown kind")
	})

	t.Run("non-owners and anonymous are refused", func(t *testing.T) {
		e := seeded(t)
		e.grant(t, e.board.ID, "writer@example.com", model.RoleWrite)
		e.grant(t, e.board.ID, "reader@example.com", model.RoleRead)
		for _, tc := range []struct{ name, user string }{
			{"writer", "writer@example.com"},
			{"reader", "reader@example.com"},
			{"stranger", "stranger@example.com"},
			{"anonymous", ""},
		} {
			e.user = tc.user
			rr := e.do(http.MethodPost, "/b/demo/access", form("kind", "user", "id", "x@example.com", "role", "read"))
			if rr.Code != http.StatusForbidden {
				t.Errorf("POST as %s = %d, want 403", tc.name, rr.Code)
			}
		}
	})

	t.Run("an unknown board slug is a 404", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/b/nope/access", form("kind", "user", "id", "x@example.com", "role", "read")),
			http.StatusNotFound)
	})
}

func TestDeleteGrant(t *testing.T) {
	t.Run("owner removes a non-owner grant", func(t *testing.T) {
		e := seeded(t)
		e.grant(t, e.board.ID, "reader@example.com", model.RoleRead)
		rr := e.do(http.MethodPost, "/b/demo/access/delete", form("kind", "user", "id", "reader@example.com"))
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		for _, g := range grantsOf(t, e, e.board.ID) {
			if g.Principal == model.UserPrincipal("reader@example.com") {
				t.Error("the grant was not removed")
			}
		}
	})

	t.Run("the last owner grant cannot be removed", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/b/demo/access/delete", form("kind", "user", "id", "owner@example.com")),
			http.StatusBadRequest, "a board must keep an owner")
		found := false
		for _, g := range grantsOf(t, e, e.board.ID) {
			if g.Principal == model.UserPrincipal("owner@example.com") && g.Role == model.RoleOwner {
				found = true
			}
		}
		if !found {
			t.Error("the last owner grant was removed after all")
		}
	})

	t.Run("non-owners and anonymous are refused", func(t *testing.T) {
		e := seeded(t)
		e.grant(t, e.board.ID, "writer@example.com", model.RoleWrite)
		e.grant(t, e.board.ID, "reader@example.com", model.RoleRead)
		for _, tc := range []struct{ name, user string }{
			{"writer", "writer@example.com"},
			{"reader", "reader@example.com"},
			{"anonymous", ""},
		} {
			e.user = tc.user
			rr := e.do(http.MethodPost, "/b/demo/access/delete", form("kind", "user", "id", "owner@example.com"))
			if rr.Code != http.StatusForbidden {
				t.Errorf("POST delete as %s = %d, want 403", tc.name, rr.Code)
			}
		}
	})

	t.Run("an unknown board slug is a 404", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/b/nope/access/delete", form("kind", "user", "id", "x@example.com")),
			http.StatusNotFound)
	})
}
