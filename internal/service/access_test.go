package service

import (
	"context"
	"testing"

	"byge/internal/identity"
	"byge/internal/model"
)

// grant gives a principal a role on a board, straight through the store,
// because handing out roles through the service is a later change.
func grant(t *testing.T, k *Byge, board model.ID, p model.Principal, role model.Role) {
	t.Helper()
	g := &model.Grant{BoardID: board, Principal: p, Role: role, CreatedAt: fixed, UpdatedAt: fixed}
	if err := k.store.SetGrant(context.Background(), g); err != nil {
		t.Fatalf("SetGrant(%s, %s): %v", p, role, err)
	}
}

func mustMakeBoard(t *testing.T, k *Byge, name string) *model.Board {
	t.Helper()
	b, err := k.CreateBoard(context.Background(), name, "", nil)
	if err != nil {
		t.Fatalf("CreateBoard(%q): %v", name, err)
	}
	return b
}

// A caller is their address, the groups kept here that hold the address, and
// the groups the provider sent. Anything with no address is none of those.
func TestPrincipals(t *testing.T) {
	k := newSvc(t)
	ctx := context.Background()

	g := &model.Group{ID: "grp1", Name: "Platform", Members: []string{"ada@example.com"}, CreatedAt: fixed, UpdatedAt: fixed}
	if err := k.store.CreateGroup(ctx, g); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	ps, err := k.Principals(ctx, identity.User{Email: "Ada@Example.com", Groups: []string{"platform-team"}})
	if err != nil {
		t.Fatalf("Principals: %v", err)
	}
	want := []model.Principal{
		{Kind: model.PrincipalUser, ID: "ada@example.com"},
		{Kind: model.PrincipalIDP, ID: "platform-team"},
		{Kind: model.PrincipalGroup, ID: "grp1"},
	}
	if len(ps) != len(want) {
		t.Fatalf("Principals = %v, want %v", ps, want)
	}
	for i := range want {
		if ps[i] != want[i] {
			t.Errorf("Principals[%d] = %v, want %v", i, ps[i], want[i])
		}
	}

	// Nobody, and a machine, are no principals. A service token that counted
	// as a principal would reach whatever its deployment granted the empty
	// address, and a group name beside no address is a header with nobody
	// behind it.
	for _, u := range []identity.User{
		{},
		{Service: "ci-token"},
		{Service: "ci-token", Groups: []string{"platform-team"}},
		{Groups: []string{"platform-team"}},
	} {
		ps, err = k.Principals(ctx, u)
		if err != nil {
			t.Fatalf("Principals(%+v): %v", u, err)
		}
		if len(ps) != 0 {
			t.Errorf("Principals(%+v) = %v, want none", u, ps)
		}
	}
}

// RoleOn reduces every principal to the one thing the caller may do, and
// require turns that into a refusal.
func TestRoleOnAndRequire(t *testing.T) {
	k := newSvc(t)
	ctx := context.Background()
	b := mustMakeBoard(t, k, "Board")

	ada := identity.User{Email: "ada@example.com", Groups: []string{"platform"}}

	// No grant is no role, whoever is asking.
	role, err := k.RoleOn(ctx, ada, b.ID)
	if err != nil {
		t.Fatalf("RoleOn: %v", err)
	}
	if role != "" {
		t.Errorf("RoleOn with no grant = %q, want none", role)
	}

	// The strongest of the principals wins, whichever one carries it.
	grant(t, k, b.ID, model.UserPrincipal("ada@example.com"), model.RoleRead)
	grant(t, k, b.ID, model.IDPPrincipal("platform"), model.RoleOwner)
	if role, err = k.RoleOn(ctx, ada, b.ID); err != nil || role != model.RoleOwner {
		t.Fatalf("RoleOn = %q, %v, want owner", role, err)
	}

	// require reads the caller out of the context, so a context with nobody
	// in it holds nothing.
	withAda := identity.NewContext(ctx, ada)
	for _, need := range []model.Role{model.RoleRead, model.RoleWrite, model.RoleOwner} {
		if err = k.require(withAda, b.ID, need); err != nil {
			t.Errorf("require(owner, %q) = %v, want nil", need, err)
		}
		if err = k.require(ctx, b.ID, need); err != ErrForbidden {
			t.Errorf("require(nobody, %q) = %v, want ErrForbidden", need, err)
		}
	}

	// A reader is refused everything above reading.
	bea := identity.NewContext(ctx, identity.User{Email: "bea@example.com"})
	grant(t, k, b.ID, model.UserPrincipal("bea@example.com"), model.RoleRead)
	if err = k.require(bea, b.ID, model.RoleRead); err != nil {
		t.Errorf("require(reader, read) = %v, want nil", err)
	}
	for _, need := range []model.Role{model.RoleWrite, model.RoleOwner} {
		if err = k.require(bea, b.ID, need); err != ErrForbidden {
			t.Errorf("require(reader, %q) = %v, want ErrForbidden", need, err)
		}
	}

	// A board the caller holds nothing on refuses even a reader of another
	// board, and so does a board that does not exist.
	other := mustMakeBoard(t, k, "Other")
	if err = k.require(bea, other.ID, model.RoleRead); err != ErrForbidden {
		t.Errorf("require on another board = %v, want ErrForbidden", err)
	}
	if err = k.require(bea, model.ID("nope"), model.RoleRead); err != ErrForbidden {
		t.Errorf("require on a board that is not there = %v, want ErrForbidden", err)
	}
}

// VisibleBoards is what the board list and the search read instead of asking
// per board.
func TestVisibleBoards(t *testing.T) {
	k := newSvc(t)
	ctx := context.Background()
	one := mustMakeBoard(t, k, "One")
	two := mustMakeBoard(t, k, "Two")
	mustMakeBoard(t, k, "Three")

	ada := identity.User{Email: "ada@example.com"}
	grant(t, k, one.ID, model.UserPrincipal("ada@example.com"), model.RoleWrite)
	grant(t, k, two.ID, model.UserPrincipal("ada@example.com"), model.RoleRead)

	got, err := k.VisibleBoards(ctx, ada)
	if err != nil {
		t.Fatalf("VisibleBoards: %v", err)
	}
	if len(got) != 2 || got[one.ID] != model.RoleWrite || got[two.ID] != model.RoleRead {
		t.Errorf("VisibleBoards = %v", got)
	}

	// Nobody sees nothing, and the empty answer is a map rather than nil so
	// a caller can index it without checking.
	got, err = k.VisibleBoards(ctx, identity.User{})
	if err != nil {
		t.Fatalf("VisibleBoards(nobody): %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("VisibleBoards(nobody) = %v, want an empty map", got)
	}
}
