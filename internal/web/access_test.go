package web

import (
	"net/http"
	"strings"
	"testing"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/service"
	"byge/internal/store/memory"
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

// The add forms check what they are given and say why beside the field, with
// the typed value still in it; the rows change a role in place; the only owner
// can be neither demoted nor removed; and the redirect after a write goes
// where the caller can still go.
func TestAccessPageForms(t *testing.T) {
	t.Run("a user that is not an address keeps what was typed", func(t *testing.T) {
		e := seeded(t)
		before := len(grantsOf(t, e, e.board.ID))
		want(t, e.do(http.MethodPost, "/b/demo/access", form("kind", "user", "id", "sapm", "role", "write")),
			http.StatusBadRequest, `value="sapm"`, `aria-invalid="true"`, `aria-describedby="add-user-error"`,
			`id="add-user-error"`,
			"not an address; a user is the address they sign in with, such as ada@example.com")
		if len(grantsOf(t, e, e.board.ID)) != before {
			t.Error("a grant was written for sapm")
		}
	})

	t.Run("an address is trimmed and lowercased", func(t *testing.T) {
		e := seeded(t)
		rr := e.do(http.MethodPost, "/b/demo/access", form("kind", "user", "id", " Ada@Example.com ", "role", "read"))
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		found := false
		for _, g := range grantsOf(t, e, e.board.ID) {
			found = found || g.Principal == model.UserPrincipal("ada@example.com")
		}
		if !found {
			t.Error("user:ada@example.com was not stored")
		}
	})

	t.Run("a byte that is not UTF-8 is refused before it is lowercased", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/b/demo/access", form("kind", "user", "id", "ada\xff@example.com", "role", "read")),
			http.StatusBadRequest, "must be text")
	})

	t.Run("a role refused on an add form marks the role select", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodPost, "/b/demo/access", form("kind", "idp", "id", "ops", "role", "admin")),
			http.StatusBadRequest, `aria-describedby="add-idp-error"`, "must be read, write or owner", `value="ops"`)
	})

	t.Run("the page suggests known principals", func(t *testing.T) {
		e := seeded(t,
			WithViewers([]identity.User{{Email: "ada@example.com", Name: "Ada Lovelace"}}),
			WithAvatars(map[string]string{"grace@example.com": "grace"}),
			WithServiceGroup("aemtli"))
		want(t, e.do(http.MethodGet, "/b/demo/access", nil), http.StatusOK,
			`<datalist id="grant-users">`, `<datalist id="grant-idp-groups">`,
			`<option value="ada@example.com">Ada Lovelace</option>`,
			`<option value="grace@example.com">`,
			`<option value="aemtli">service caller</option>`,
			`placeholder="aemtli"`, "aemtli is the in-cluster service caller")
	})

	t.Run("a granted principal is not suggested", func(t *testing.T) {
		e := seeded(t, WithViewers([]identity.User{{Email: "ada@example.com", Name: "Ada Lovelace"}}))
		e.grant(t, e.board.ID, "ada@example.com", model.RoleRead)
		rr := e.do(http.MethodGet, "/b/demo/access", nil)
		want(t, rr, http.StatusOK)
		if strings.Contains(rr.Body.String(), `<option value="ada@example.com">`) {
			t.Error("ada is granted here and still suggested")
		}
	})

	t.Run("a local group is picked by name", func(t *testing.T) {
		e := seeded(t)
		g, err := e.svc.CreateGroup(ownerCtx(), "Platform", []string{"ada@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		rr := e.do(http.MethodGet, "/b/demo/access", nil)
		want(t, rr, http.StatusOK, `<option value="`+string(g.ID)+`">Platform</option>`,
			`<option value="">pick a group</option>`, `<select name="id" required aria-label="Local group"`)
		if strings.Contains(rr.Body.String(), "named by its id") {
			t.Error("the help text still says a local group is named by its id")
		}
		// And a granted group's row shows its name rather than its id.
		if rr := e.do(http.MethodPost, "/b/demo/access", form("kind", "group", "id", string(g.ID), "role", "read")); rr.Code != http.StatusSeeOther {
			t.Fatalf("grant the group: %d", rr.Code)
		}
		want(t, e.do(http.MethodGet, "/b/demo/access", nil), http.StatusOK, "Remove local group Platform?")
	})

	t.Run("no local groups yet links to the groups page", func(t *testing.T) {
		e := seeded(t)
		want(t, e.do(http.MethodGet, "/b/demo/access", nil), http.StatusOK,
			"No local groups yet.", `<a href="/groups" class="underline">`)
	})

	t.Run("an owner who is not the instance owner can still add a local group", func(t *testing.T) {
		e := seeded(t)
		g, err := e.svc.CreateGroup(ownerCtx(), "Platform", []string{"ada@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		e.grant(t, e.board.ID, "second@example.com", model.RoleOwner)
		e.user = "second@example.com"
		want(t, e.do(http.MethodGet, "/b/demo/access", nil), http.StatusOK,
			`aria-label="Local group id"`, "listed for the instance owner only")
		rr := e.do(http.MethodPost, "/b/demo/access", form("kind", "group", "id", string(g.ID), "role", "read"))
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		found := false
		for _, gr := range grantsOf(t, e, e.board.ID) {
			found = found || gr.Principal == model.GroupPrincipal(g.ID)
		}
		if !found {
			t.Error("the group grant was not written")
		}
	})

	t.Run("a row changes its role in place", func(t *testing.T) {
		e := seeded(t)
		e.grant(t, e.board.ID, "reader@example.com", model.RoleRead)
		want(t, e.do(http.MethodGet, "/b/demo/access", nil), http.StatusOK,
			`name="from" value="row"`, "row-save", `aria-label="Role of reader@example.com"`)
		rr := e.do(http.MethodPost, "/b/demo/access",
			form("kind", "user", "id", "reader@example.com", "role", "write", "from", "row"))
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/b/demo/access" {
			t.Fatalf("status = %d, Location = %q", rr.Code, rr.Header().Get("Location"))
		}
		for _, g := range grantsOf(t, e, e.board.ID) {
			if g.Principal == model.UserPrincipal("reader@example.com") && g.Role != model.RoleWrite {
				t.Errorf("role = %q, want write", g.Role)
			}
		}
	})

	t.Run("the only owner has no bin and cannot be demoted", func(t *testing.T) {
		e := seeded(t)
		rr := e.do(http.MethodGet, "/b/demo/access", nil)
		want(t, rr, http.StatusOK, "the only owner")
		if strings.Contains(rr.Body.String(), "Remove user owner@example.com?") {
			t.Error("the only owner has a bin")
		}
		want(t, e.do(http.MethodPost, "/b/demo/access",
			form("kind", "user", "id", ownerEmail, "role", "read", "from", "row")),
			http.StatusBadRequest, "a board must keep an owner", `id="grant-0-error"`)
		for _, g := range grantsOf(t, e, e.board.ID) {
			if g.Principal == model.UserPrincipal(ownerEmail) && g.Role != model.RoleOwner {
				t.Errorf("the only owner was demoted to %q", g.Role)
			}
		}
	})

	t.Run("demoting yourself goes to the board", func(t *testing.T) {
		e := seeded(t)
		e.grant(t, e.board.ID, "second@example.com", model.RoleOwner)
		rr := e.do(http.MethodPost, "/b/demo/access",
			form("kind", "user", "id", ownerEmail, "role", "write", "from", "row"))
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/b/demo" {
			t.Fatalf("status = %d, Location = %q", rr.Code, rr.Header().Get("Location"))
		}
	})

	t.Run("removing yourself goes to the board list", func(t *testing.T) {
		e := seeded(t)
		e.grant(t, e.board.ID, "second@example.com", model.RoleOwner)
		rr := e.do(http.MethodPost, "/b/demo/access/delete", form("kind", "user", "id", ownerEmail))
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/boards" {
			t.Fatalf("status = %d, Location = %q", rr.Code, rr.Header().Get("Location"))
		}
	})
}

// Behind a proxy a user may be a login and an idp group has no comma, and the
// page says so in its fields and its help text.
func TestAccessPageInProxyMode(t *testing.T) {
	st := memory.New()
	svc := service.New(st, service.WithProxyIdentity(true), service.WithBootstrapOwner(ownerEmail))
	if _, err := svc.CreateBoard(ownerCtx(), "Demo Board", "demo", nil); err != nil {
		t.Fatal(err)
	}
	e := &env{h: New(svc, nil, nil), svc: svc, st: st, user: ownerEmail}
	want(t, e.do(http.MethodGet, "/b/demo/access", nil), http.StatusOK,
		`placeholder="address or login"`, `pattern="[^,]+"`, "or the login your proxy sends")
	rr := e.do(http.MethodPost, "/b/demo/access", form("kind", "user", "id", "u236858", "role", "read"))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("login u236858: status = %d: %s", rr.Code, rr.Body.String())
	}
	want(t, e.do(http.MethodPost, "/b/demo/access", form("kind", "idp", "id", "platform, ops", "role", "read")),
		http.StatusBadRequest, "cannot contain a comma", `aria-describedby="add-idp-error"`)
}
