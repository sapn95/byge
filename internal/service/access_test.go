package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/store/memory"
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
	// Create under the owner context so the board has a caller that owns it;
	// its grant() helper then adds any extra roles the test needs. The behaviour
	// tests check specific other principals, which owner@example.com's grant
	// does not touch.
	b, err := k.CreateBoard(ownerCtx(), name, "", nil)
	if err != nil {
		t.Fatalf("CreateBoard(%q): %v", name, err)
	}
	return b
}

// A caller is their address, the groups kept here that hold the address, and
// the groups the provider sent. A caller with no address carries no user
// principal and no local-group membership, but it does carry any idp groups
// that arrived with the request — the service identity is exactly that. A
// non-person with no groups still holds nothing.
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

	// A non-person with no groups is still no principals: a Cloudflare service
	// token that counted as a principal would reach whatever its deployment
	// granted the empty address. That is the hole stages 3-5 closed, and it
	// stays closed.
	for _, u := range []identity.User{
		{},
		{Service: "ci-token"},
	} {
		ps, err = k.Principals(ctx, u)
		if err != nil {
			t.Fatalf("Principals(%+v): %v", u, err)
		}
		if len(ps) != 0 {
			t.Errorf("Principals(%+v) = %v, want none", u, ps)
		}
	}

	// A non-person that carries idp groups holds exactly those, as idp
	// principals, and nothing else: no user principal, no local-group lookup.
	// This is the narrowed invariant the service identity rides on.
	for _, u := range []identity.User{
		{Service: "ci-token", Groups: []string{"platform-team"}},
		{Groups: []string{"platform-team"}},
	} {
		ps, err = k.Principals(ctx, u)
		if err != nil {
			t.Fatalf("Principals(%+v): %v", u, err)
		}
		want := []model.Principal{{Kind: model.PrincipalIDP, ID: "platform-team"}}
		if len(ps) != len(want) || ps[0] != want[0] {
			t.Errorf("Principals(%+v) = %v, want %v", u, ps, want)
		}
	}
}

// The service identity is a non-person carrying one configured idp group. It
// gets exactly that group as an idp principal, with no user principal and no
// local-group lookup, and nothing at all when it carries no group.
func TestServiceCallerPrincipals(t *testing.T) {
	k := newSvc(t)
	ctx := context.Background()

	ps, err := k.Principals(ctx, identity.User{Service: "aemtli", Groups: []string{"service"}})
	if err != nil {
		t.Fatalf("Principals: %v", err)
	}
	want := []model.Principal{{Kind: model.PrincipalIDP, ID: "service"}}
	if len(ps) != len(want) || ps[0] != want[0] {
		t.Errorf("Principals(service with group) = %v, want %v", ps, want)
	}

	ps, err = k.Principals(ctx, identity.User{Service: "aemtli"})
	if err != nil {
		t.Fatalf("Principals(service no group): %v", err)
	}
	if len(ps) != 0 {
		t.Errorf("Principals(service no group) = %v, want none", ps)
	}
}

// The service caller is admitted on a board where its idp group holds a write
// grant, and refused (403) on a board where it does not. This is the whole
// point of the feature: a grantable service identity that the stage-3-5 model
// admits only where granted.
func TestServiceCallerGrantedWriteOnlyWhereGranted(t *testing.T) {
	k := newSvc(t)
	board := mustMakeBoard(t, k, "Granted")
	other := mustMakeBoard(t, k, "Not granted")

	grant(t, k, board.ID, model.IDPPrincipal("service"), model.RoleWrite)

	serviceCtx := identity.NewContext(context.Background(),
		identity.User{Service: "aemtli", Groups: []string{"service"}})

	if err := k.require(serviceCtx, board.ID, model.RoleWrite); err != nil {
		t.Errorf("require on the granted board = %v, want nil", err)
	}
	if err := k.require(serviceCtx, other.ID, model.RoleWrite); err != ErrForbidden {
		t.Errorf("require on the ungranted board = %v, want ErrForbidden", err)
	}

	// And the grant is what admits it: proven through a write that reaches the
	// store, and refused on the other board.
	if _, err := k.CreateCard(serviceCtx, board.ID, board.Columns[0].ID, CardInput{Title: "from aemtli"}); err != nil {
		t.Errorf("service CreateCard on the granted board = %v, want nil", err)
	}
	if _, err := k.CreateCard(serviceCtx, other.ID, other.Columns[0].ID, CardInput{Title: "x"}); err != ErrForbidden {
		t.Errorf("service CreateCard on the ungranted board = %v, want ErrForbidden", err)
	}
}

// The service caller is not a person, so it is never the bootstrap owner even
// with the header set: it cannot claim an ownerless board and cannot manage
// groups. Person()-gated predicates (addrIsBootstrapOwner, OwnerlessBoards,
// ClaimBoard, requireGroupAdmin) all refuse it.
func TestServiceCallerIsNotAPerson(t *testing.T) {
	n := 0
	k := New(memory.New(),
		WithClock(func() time.Time { return fixed }),
		WithIDs(func() model.ID { n++; return model.ID(fmt.Sprintf("id%03d", n)) }),
		WithBootstrapOwner("owner@example.com"))

	service := identity.User{Service: "aemtli", Groups: []string{"service"}}
	serviceCtx := identity.NewContext(context.Background(), service)

	if k.addrIsBootstrapOwner(service) {
		t.Error("the service caller reads as the bootstrap owner")
	}

	b, err := k.CreateBoard(ownerCtx(), "Board", "", nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	// An ownerless board the bootstrap owner could claim; the service caller
	// cannot even see it, and cannot claim it.
	ownerless, err := k.EnsureDefaultBoard(context.Background())
	if err != nil {
		t.Fatalf("EnsureDefaultBoard: %v", err)
	}
	if got, err := k.OwnerlessBoards(serviceCtx); err != nil || got != nil {
		t.Errorf("OwnerlessBoards(service) = %v, %v, want nil", got, err)
	}
	if err := k.ClaimBoard(serviceCtx, ownerless[0].ID); err != ErrForbidden {
		t.Errorf("ClaimBoard(service) = %v, want ErrForbidden", err)
	}

	// Groups management is the bootstrap owner alone; the service caller is
	// refused even on a board it was granted write on.
	grant(t, k, b.ID, model.IDPPrincipal("service"), model.RoleWrite)
	if err := k.requireGroupAdmin(serviceCtx); err != ErrForbidden {
		t.Errorf("requireGroupAdmin(service) = %v, want ErrForbidden", err)
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

// VisibleBoardList is Boards filtered to what the caller may read: the list a
// layer draws, as opposed to Boards which lists every board regardless of role.
// A caller holding one grant gets that one board; a caller holding nothing gets
// an empty list and not every board.
func TestVisibleBoardList(t *testing.T) {
	k := newSvc(t)
	one := mustMakeBoard(t, k, "One")
	mustMakeBoard(t, k, "Two")
	mustMakeBoard(t, k, "Three")

	ada := identity.NewContext(context.Background(), identity.User{Email: "ada@example.com"})
	grant(t, k, one.ID, model.UserPrincipal("ada@example.com"), model.RoleRead)

	list, err := k.VisibleBoardList(ada)
	if err != nil {
		t.Fatalf("VisibleBoardList: %v", err)
	}
	if len(list) != 1 || list[0].ID != one.ID {
		t.Errorf("VisibleBoardList = %+v, want only %q", list, one.ID)
	}

	// A caller who holds nothing sees nothing, even though three boards exist.
	// Boards would hand all three back; this is the method that does not.
	list, err = k.VisibleBoardList(context.Background())
	if err != nil {
		t.Fatalf("VisibleBoardList(nobody): %v", err)
	}
	if len(list) != 0 {
		t.Errorf("VisibleBoardList(nobody) = %+v, want none", list)
	}
	if all, _ := k.Boards(context.Background()); len(all) != 3 {
		t.Errorf("Boards = %d, the filter must be VisibleBoardList and not Boards", len(all))
	}
}

// readerCtx, writerCtx and ownerBoard set up the one shape every method in the
// matrix below needs: a board owned by owner@example.com (mustMakeBoard does
// that) with one more caller granted a single role on it. The caller is a
// person, in a context, because require reads identity out of the context.
func grantedCtx(t *testing.T, k *Byge, board model.ID, email string, role model.Role) context.Context {
	t.Helper()
	grant(t, k, board, model.UserPrincipal(email), role)
	return identity.NewContext(context.Background(), identity.User{Email: email})
}

// A reader opens the board and reads its cards and is refused everything that
// writes or owns. require runs before any board or card lookup, so a refused
// write names the forbidden role rather than a missing column.
func TestRoleMatrixReader(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	reader := grantedCtx(t, k, b.ID, "reader@example.com", model.RoleRead)

	if _, err := k.Cards(reader, b.ID); err != nil {
		t.Errorf("reader Cards = %v, want nil", err)
	}
	if _, err := k.CreateCard(reader, b.ID, b.Columns[0].ID, CardInput{Title: "x"}); err != ErrForbidden {
		t.Errorf("reader CreateCard = %v, want ErrForbidden", err)
	}
	if _, err := k.AddColumn(reader, b.ID, "New", 0, false); err != ErrForbidden {
		t.Errorf("reader AddColumn = %v, want ErrForbidden", err)
	}
	if _, err := k.RenameBoard(reader, b.ID, "Renamed", ""); err != ErrForbidden {
		t.Errorf("reader RenameBoard = %v, want ErrForbidden", err)
	}
}

// A writer adds and moves cards and is refused only the owner's settings
// changes. The write path runs all the way to the store, so the card it
// creates is proof the guard let it through rather than failed open.
func TestRoleMatrixWriter(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	writer := grantedCtx(t, k, b.ID, "writer@example.com", model.RoleWrite)

	c, err := k.CreateCard(writer, b.ID, b.Columns[0].ID, CardInput{Title: "a card"})
	if err != nil {
		t.Fatalf("writer CreateCard = %v, want nil", err)
	}
	if c == nil || c.Title != "a card" {
		t.Fatalf("writer CreateCard card = %+v", c)
	}
	if _, err := k.AddColumn(writer, b.ID, "New", 0, false); err != ErrForbidden {
		t.Errorf("writer AddColumn = %v, want ErrForbidden", err)
	}
	if _, err := k.RenameBoard(writer, b.ID, "Renamed", ""); err != ErrForbidden {
		t.Errorf("writer RenameBoard = %v, want ErrForbidden", err)
	}
}

// An owner may do all three. owner@example.com owns the board through
// CreateBoard's self-grant, so ownerCtx is already an owner here.
func TestRoleMatrixOwner(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")
	owner := ownerCtx()

	if _, err := k.Cards(owner, b.ID); err != nil {
		t.Errorf("owner Cards = %v, want nil", err)
	}
	if _, err := k.CreateCard(owner, b.ID, b.Columns[0].ID, CardInput{Title: "x"}); err != nil {
		t.Errorf("owner CreateCard = %v, want nil", err)
	}
	if _, err := k.AddColumn(owner, b.ID, "New", 0, false); err != nil {
		t.Errorf("owner AddColumn = %v, want nil", err)
	}
}

// An unidentified caller and a service token are no principals, so they hold
// nothing on any board: every guarded method refuses them, and only the open
// methods answer. A service token that could read a board the moment grants
// existed is the hole this change closes, so it is checked beside the
// anonymous one.
func TestNoPrincipalsRefusedButOpenMethodsAnswer(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"anonymous", context.Background()},
		{"service token", identity.NewContext(context.Background(), identity.User{Service: "ci"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := k.Cards(tc.ctx, b.ID); err != ErrForbidden {
				t.Errorf("Cards = %v, want ErrForbidden", err)
			}
			if _, err := k.CreateCard(tc.ctx, b.ID, b.Columns[0].ID, CardInput{Title: "x"}); err != ErrForbidden {
				t.Errorf("CreateCard = %v, want ErrForbidden", err)
			}
			if _, err := k.AddColumn(tc.ctx, b.ID, "New", 0, false); err != ErrForbidden {
				t.Errorf("AddColumn = %v, want ErrForbidden", err)
			}
			// Boards is open to everyone: it lists the board for a caller who
			// may not read it, because the list filters per board elsewhere.
			list, err := k.Boards(tc.ctx)
			if err != nil || len(list) != 1 {
				t.Errorf("Boards = %+v, %v, want one board and no error", list, err)
			}
		})
	}
}

// Search is single-board: it refuses an unreadable board with ErrForbidden
// rather than filtering it away, because the filter-not-refuse rule lives in
// Boards and VisibleBoards, which this is not.
func TestSearchRefusesAnUnreadableBoard(t *testing.T) {
	k := newSvc(t)
	b := mustMakeBoard(t, k, "Board")

	stranger := identity.NewContext(context.Background(), identity.User{Email: "stranger@example.com"})
	if _, err := k.Search(stranger, b.ID, Query{}); err != ErrForbidden {
		t.Errorf("Search on a board the caller holds nothing on = %v, want ErrForbidden", err)
	}
	// The owner reads it, which is the proof the refusal is the grant and not
	// the board.
	if _, err := k.Search(ownerCtx(), b.ID, Query{}); err != nil {
		t.Errorf("Search as owner = %v, want nil", err)
	}
}

// CreateBoard is open to any person and to nobody else. An anonymous context
// is refused and writes no board; an identified caller becomes the board's
// owner, which is the grant that lets them do anything with it afterwards.
func TestCreateBoardIdentityAndOwnerGrant(t *testing.T) {
	k := newSvc(t)

	if _, err := k.CreateBoard(context.Background(), "Anon", "", nil); err != ErrForbidden {
		t.Errorf("CreateBoard anonymous = %v, want ErrForbidden", err)
	}
	list, err := k.Boards(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("Boards after refused create = %+v, %v, want none", list, err)
	}

	ada := identity.User{Email: "ada@example.com"}
	ctx := identity.NewContext(context.Background(), ada)
	b, err := k.CreateBoard(ctx, "Ada's", "", nil)
	if err != nil {
		t.Fatalf("CreateBoard identified = %v, want nil", err)
	}
	role, err := k.RoleOn(ctx, ada, b.ID)
	if err != nil {
		t.Fatalf("RoleOn: %v", err)
	}
	if role != model.RoleOwner {
		t.Errorf("creator RoleOn = %q, want owner", role)
	}
}

// EnsureDefaultBoard is the bootstrap, so it runs on a plain context with no
// identity, creates the board without error, and grants nobody anything: a
// default board has no human owner until stage 5's onboarding, so RoleOn of a
// caller on it is "".
func TestEnsureDefaultBoardBootstrapNoGrant(t *testing.T) {
	k := newSvc(t)
	ctx := context.Background()

	boards, err := k.EnsureDefaultBoard(ctx)
	if err != nil {
		t.Fatalf("EnsureDefaultBoard = %v, want nil", err)
	}
	if len(boards) != 1 {
		t.Fatalf("EnsureDefaultBoard returned %d boards, want 1", len(boards))
	}
	role, err := k.RoleOn(ctx, identity.User{Email: "anyone@example.com"}, boards[0].ID)
	if err != nil {
		t.Fatalf("RoleOn: %v", err)
	}
	if role != "" {
		t.Errorf("RoleOn of a caller on the bootstrap board = %q, want none", role)
	}
}
