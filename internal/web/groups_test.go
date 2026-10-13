package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"byge/internal/identity"
	"byge/internal/model"
)

// The local groups page and its three writes. Only the bootstrap owner may
// manage groups; an anonymous caller, a service token and any other signed-in
// caller are refused. The default request caller is the bootstrap owner (see
// newEnv), so the write sub-tests below pass as that caller. Every route is
// requested.

// groupsList reads the groups straight from the store, to assert what a write
// did without the admin guard in the way.
func groupsList(t *testing.T, e *env) []model.Group {
	t.Helper()
	g, err := e.st.ListGroups(ownerCtx())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// doService makes a request carrying a service-token identity, which e.do
// cannot build because it only ever sets an address.
func (e *env) doService(method, path string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if body != nil && method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req = req.WithContext(identity.NewContext(req.Context(), identity.User{Service: "ci"}))
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, req)
	return rr
}

func TestGroupsPageAccess(t *testing.T) {
	e := seeded(t)

	// The bootstrap owner (the default caller) sees the page.
	want(t, e.do(http.MethodGet, "/groups", nil), http.StatusOK, "Local groups")

	// Another identified caller, who is not the bootstrap owner, is refused.
	e.user = "someone@example.com"
	if rr := e.do(http.MethodGet, "/groups", nil); rr.Code != http.StatusForbidden {
		t.Errorf("non-bootstrap caller GET /groups = %d, want 403", rr.Code)
	}

	// A service token is refused.
	if rr := e.doService(http.MethodGet, "/groups", nil); rr.Code != http.StatusForbidden {
		t.Errorf("service token GET /groups = %d, want 403", rr.Code)
	}

	// Anonymous is refused.
	e.user = ""
	if rr := e.do(http.MethodGet, "/groups", nil); rr.Code != http.StatusForbidden {
		t.Errorf("anonymous GET /groups = %d, want 403", rr.Code)
	}
}

func TestCreateGroupThroughTheWeb(t *testing.T) {
	t.Run("a valid name is created", func(t *testing.T) {
		e := seeded(t)
		rr := e.do(http.MethodPost, "/groups", form("name", "Platform"))
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/groups" {
			t.Fatalf("status = %d, Location = %q", rr.Code, rr.Header().Get("Location"))
		}
		if len(groupsList(t, e)) != 1 {
			t.Error("the group was not created")
		}
	})

	t.Run("members are lowercased and deduped", func(t *testing.T) {
		e := seeded(t)
		rr := e.do(http.MethodPost, "/groups", form("name", "Platform", "members", "Ada@Example.com\nada@example.com\nbob@example.com"))
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		g := groupsList(t, e)
		if len(g) != 1 || len(g[0].Members) != 2 {
			t.Fatalf("members = %+v, want two, lowercased and deduped", g)
		}
		for _, m := range g[0].Members {
			if m != "ada@example.com" && m != "bob@example.com" {
				t.Errorf("member %q was not normalised", m)
			}
		}
	})

	t.Run("an empty name is a 400", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/groups", form("name", "  ")), http.StatusBadRequest)
	})

	t.Run("a duplicate name is a 409", func(t *testing.T) {
		e := seeded(t)
		if rr := e.do(http.MethodPost, "/groups", form("name", "Platform")); rr.Code != http.StatusSeeOther {
			t.Fatal(rr.Code)
		}
		want(t, e.do(http.MethodPost, "/groups", form("name", "Platform")), http.StatusConflict, "already exists")
	})

	t.Run("anonymous is refused", func(t *testing.T) {
		e := seeded(t)
		e.user = ""
		if rr := e.do(http.MethodPost, "/groups", form("name", "Platform")); rr.Code != http.StatusForbidden {
			t.Errorf("anonymous POST /groups = %d, want 403", rr.Code)
		}
		if len(groupsList(t, e)) != 0 {
			t.Error("an anonymous caller created a group")
		}
	})
}

func TestUpdateGroupThroughTheWeb(t *testing.T) {
	t.Run("rename and new members", func(t *testing.T) {
		e := seeded(t)
		g, err := e.svc.CreateGroup(ownerCtx(), "Platform", []string{"old@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		rr := e.do(http.MethodPost, "/groups/"+string(g.ID), form("name", "Renamed", "members", "new@example.com"))
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		list := groupsList(t, e)
		if len(list) != 1 || list[0].Name != "Renamed" {
			t.Fatalf("groups = %+v, want one named Renamed", list)
		}
		if len(list[0].Members) != 1 || list[0].Members[0] != "new@example.com" {
			t.Errorf("members = %v, want the membership replaced", list[0].Members)
		}
	})

	t.Run("an unknown id is a 404", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/groups/nope", form("name", "Renamed")), http.StatusNotFound)
	})

	t.Run("anonymous is refused", func(t *testing.T) {
		e := seeded(t)
		g, err := e.svc.CreateGroup(ownerCtx(), "Platform", nil)
		if err != nil {
			t.Fatal(err)
		}
		e.user = ""
		if rr := e.do(http.MethodPost, "/groups/"+string(g.ID), form("name", "Renamed")); rr.Code != http.StatusForbidden {
			t.Errorf("anonymous POST /groups/{id} = %d, want 403", rr.Code)
		}
	})
}

func TestDeleteGroupThroughTheWeb(t *testing.T) {
	t.Run("the group and its grants go", func(t *testing.T) {
		e := seeded(t)
		g, err := e.svc.CreateGroup(ownerCtx(), "Platform", nil)
		if err != nil {
			t.Fatal(err)
		}
		// A grant to the group, so the delete has something to cascade.
		if err := e.svc.SetBoardGrant(ownerCtx(), e.board.ID, model.GroupPrincipal(g.ID), model.RoleRead); err != nil {
			t.Fatal(err)
		}
		rr := e.do(http.MethodPost, "/groups/"+string(g.ID)+"/delete", nil)
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		if len(groupsList(t, e)) != 0 {
			t.Error("the group was not deleted")
		}
		for _, gr := range grantsOf(t, e, e.board.ID) {
			if gr.Principal == model.GroupPrincipal(g.ID) {
				t.Error("a grant to the deleted group survived")
			}
		}
	})

	t.Run("anonymous is refused", func(t *testing.T) {
		e := seeded(t)
		g, err := e.svc.CreateGroup(ownerCtx(), "Platform", nil)
		if err != nil {
			t.Fatal(err)
		}
		e.user = ""
		if rr := e.do(http.MethodPost, "/groups/"+string(g.ID)+"/delete", nil); rr.Code != http.StatusForbidden {
			t.Errorf("anonymous POST /groups/{id}/delete = %d, want 403", rr.Code)
		}
	})
}
