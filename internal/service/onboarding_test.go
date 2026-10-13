package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/store"
	"byge/internal/store/memory"
)

// Stage 5 onboarding: the bootstrap owner is the only caller who may claim a
// board with no owner, and an ownerless board is reachable by nobody else. The
// mechanism lives in RoleOn, ClaimBoard, OwnerlessBoards and boardHasOwner, and
// is inert when AUTH_BOOTSTRAP_OWNER is unset. Every test here sets the caller
// with identity.NewContext and builds the service with WithBootstrapOwner, the
// same path cmd/byge wires from config.

// bootstrapSvc is newSvc with a bootstrap owner, the one the stage-5 suite runs
// against. The id generator and clock match newSvc so a board's id is stable.
func bootstrapSvc(t *testing.T, owner string) *Byge {
	t.Helper()
	n := 0
	ids := func() model.ID { n++; return model.ID(fmt.Sprintf("id%03d", n)) }
	return New(memory.New(),
		WithClock(func() time.Time { return fixed }),
		WithIDs(ids),
		WithBootstrapOwner(owner))
}

// makeOwnerless writes a board straight to the store with no grant, the way
// EnsureDefaultBoard does, so it is genuinely ownerless rather than owned by
// its creator.
func makeOwnerless(t *testing.T, k *Byge, slug string) *model.Board {
	t.Helper()
	b := &model.Board{
		ID: k.newID(), Slug: slug, Name: slug, Layout: model.LayoutColumns,
		SLA: model.DefaultSLA(), CreatedAt: fixed, UpdatedAt: fixed,
	}
	b.Columns = append(b.Columns, model.Column{ID: k.newID(), BoardID: b.ID, Name: "To Do"})
	if err := k.store.CreateBoard(context.Background(), b); err != nil {
		t.Fatalf("CreateBoard(%q): %v", slug, err)
	}
	return b
}

// ctxAs is a context carrying one signed-in person.
func ctxAs(email string) context.Context {
	return identity.NewContext(context.Background(), identity.User{Email: email})
}

// ownerGrantFor reports whether the board carries an owner grant for the user.
func ownerGrantFor(t *testing.T, k *Byge, boardID model.ID, email string) bool {
	t.Helper()
	grants, err := k.store.ListGrants(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	want := model.UserPrincipal(email)
	for _, g := range grants {
		if g.Principal == want && g.Role == model.RoleOwner {
			return true
		}
	}
	return false
}

// hasAnyOwner reports whether the board carries any owner grant at all.
func hasAnyOwner(t *testing.T, k *Byge, boardID model.ID) bool {
	t.Helper()
	grants, err := k.store.ListGrants(context.Background(), boardID)
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	for _, g := range grants {
		if g.Role == model.RoleOwner {
			return true
		}
	}
	return false
}

// 1. The bootstrap owner claims an ownerless board.
func TestClaimBoardByBootstrapOwner(t *testing.T) {
	k := bootstrapSvc(t, "ada@example.com")
	b := makeOwnerless(t, k, "board")

	if err := k.ClaimBoard(ctxAs("ada@example.com"), b.ID); err != nil {
		t.Fatalf("ClaimBoard = %v, want nil", err)
	}
	if !ownerGrantFor(t, k, b.ID, "ada@example.com") {
		t.Error("the claimed board has no owner grant for the bootstrap owner")
	}
}

// 2 (MUTANT). An arbitrary non-bootstrap caller cannot claim. The guard this
// pins is ClaimBoard's isBootstrapOwner check: drop it and mallory claims.
func TestClaimBoardRefusesNonBootstrapCaller(t *testing.T) {
	k := bootstrapSvc(t, "ada@example.com")
	b := makeOwnerless(t, k, "board")

	if err := k.ClaimBoard(ctxAs("mallory@example.com"), b.ID); err != ErrForbidden {
		t.Fatalf("mallory ClaimBoard = %v, want ErrForbidden", err)
	}
	if hasAnyOwner(t, k, b.ID) {
		t.Error("the board gained an owner from a refused claim")
	}
}

// 3 (MUTANT, arbitrary half). An ownerless board is invisible and unreachable
// to an arbitrary caller and reachable by the bootstrap owner. The guard this
// pins is the RoleOn bootstrap branch's isBootstrapOwner check: make it run for
// everyone and mallory's RoleOn returns RoleOwner / the board joins their list.
func TestOwnerlessBoardVisibleToBootstrapOwnerOnly(t *testing.T) {
	k := bootstrapSvc(t, "ada@example.com")
	b := makeOwnerless(t, k, "board")

	// mallory: no role, not in the visible list.
	mallory := identity.User{Email: "mallory@example.com"}
	if role, err := k.RoleOn(context.Background(), mallory, b.ID); err != nil || role != "" {
		t.Fatalf("mallory RoleOn = %q, %v, want \"\"", role, err)
	}
	list, err := k.VisibleBoardList(ctxAs("mallory@example.com"))
	if err != nil {
		t.Fatalf("VisibleBoardList: %v", err)
	}
	for _, vb := range list {
		if vb.ID == b.ID {
			t.Error("the ownerless board is in an arbitrary caller's visible list")
		}
	}

	// ada: owner through the shortcut, and the board is in OwnerlessBoards.
	ada := identity.User{Email: "ada@example.com"}
	if role, err := k.RoleOn(context.Background(), ada, b.ID); err != nil || role != model.RoleOwner {
		t.Fatalf("ada RoleOn = %q, %v, want owner", role, err)
	}
	ownerless, err := k.OwnerlessBoards(ctxAs("ada@example.com"))
	if err != nil {
		t.Fatalf("OwnerlessBoards: %v", err)
	}
	if len(ownerless) != 1 || ownerless[0].ID != b.ID {
		t.Fatalf("OwnerlessBoards = %+v, want the one ownerless board", ownerless)
	}
}

// 3a. OwnerlessBoards agrees board-for-board with the RoleOn ownerless branch,
// over a mix of owned and ownerless boards, and is nil for a non-bootstrap and
// an anonymous caller. This pins the two deciders to the shared boardHasOwner.
func TestOwnerlessBoardsAgreesWithRoleOn(t *testing.T) {
	k := bootstrapSvc(t, "ada@example.com")
	// Two ownerless boards and one owned by someone else.
	free1 := makeOwnerless(t, k, "free-one")
	free2 := makeOwnerless(t, k, "free-two")
	owned := makeOwnerless(t, k, "owned")
	grant(t, k, owned.ID, model.UserPrincipal("bob@example.com"), model.RoleOwner)

	ada := identity.User{Email: "ada@example.com"}
	ownerless, err := k.OwnerlessBoards(ctxAs("ada@example.com"))
	if err != nil {
		t.Fatalf("OwnerlessBoards: %v", err)
	}
	inList := map[model.ID]bool{}
	for _, b := range ownerless {
		inList[b.ID] = true
	}
	for _, b := range []*model.Board{free1, free2, owned} {
		role, err := k.RoleOn(context.Background(), ada, b.ID)
		if err != nil {
			t.Fatalf("RoleOn(%s): %v", b.Slug, err)
		}
		if inList[b.ID] != (role == model.RoleOwner) {
			t.Errorf("board %s: inOwnerless=%v, RoleOn=%q, the two deciders disagree", b.Slug, inList[b.ID], role)
		}
	}

	// nil for a non-bootstrap caller and for an anonymous one.
	if got, err := k.OwnerlessBoards(ctxAs("mallory@example.com")); err != nil || got != nil {
		t.Errorf("OwnerlessBoards(mallory) = %+v, %v, want nil", got, err)
	}
	if got, err := k.OwnerlessBoards(context.Background()); err != nil || got != nil {
		t.Errorf("OwnerlessBoards(anonymous) = %+v, %v, want nil", got, err)
	}
}

// 4. Anonymous and a service token claim nothing and are never the bootstrap
// owner, even with AUTH_BOOTSTRAP_OWNER set.
func TestClaimRefusesAnonymousAndServiceToken(t *testing.T) {
	k := bootstrapSvc(t, "ada@example.com")
	b := makeOwnerless(t, k, "board")

	anon := context.Background()
	svcTok := identity.NewContext(context.Background(), identity.User{Service: "ämtli"})

	if err := k.ClaimBoard(anon, b.ID); err != ErrForbidden {
		t.Errorf("anonymous ClaimBoard = %v, want ErrForbidden", err)
	}
	if err := k.ClaimBoard(svcTok, b.ID); err != ErrForbidden {
		t.Errorf("service token ClaimBoard = %v, want ErrForbidden", err)
	}
	if hasAnyOwner(t, k, b.ID) {
		t.Error("the board gained an owner from a refused claim")
	}
	if k.isBootstrapOwner(anon) {
		t.Error("isBootstrapOwner true for an anonymous caller")
	}
	if k.isBootstrapOwner(svcTok) {
		t.Error("isBootstrapOwner true for a service token")
	}
}

// 5. The caller side is matched case- and space-insensitively, because the
// config value is lowercased on read and UserPrincipal lowercases and trims the
// caller side. Every spelling of the owner's address claims successfully.
func TestClaimIsCaseAndSpaceInsensitive(t *testing.T) {
	for _, caller := range []string{"Ada@X", "ADA@X", "ada@X", "  ada@x  "} {
		k := bootstrapSvc(t, "ada@x")
		b := makeOwnerless(t, k, "board")
		if err := k.ClaimBoard(ctxAs(caller), b.ID); err != nil {
			t.Errorf("ClaimBoard as %q = %v, want nil", caller, err)
		}
	}
}

// 6. An already-owned board cannot be claimed, and its owner is unchanged.
func TestClaimRefusesAnOwnedBoard(t *testing.T) {
	k := bootstrapSvc(t, "ada@example.com")
	b := makeOwnerless(t, k, "board")
	grant(t, k, b.ID, model.UserPrincipal("bob@example.com"), model.RoleOwner)

	if err := k.ClaimBoard(ctxAs("ada@example.com"), b.ID); err != ErrForbidden {
		t.Fatalf("ClaimBoard of an owned board = %v, want ErrForbidden", err)
	}
	if ownerGrantFor(t, k, b.ID, "ada@example.com") {
		t.Error("the bootstrap owner became an owner of a board that was already owned")
	}
	if !ownerGrantFor(t, k, b.ID, "bob@example.com") {
		t.Error("bob is no longer the owner after a refused claim")
	}
}

// 7. The mechanism is inert when AUTH_BOOTSTRAP_OWNER is unset: no caller can
// claim, RoleOn is "" for everyone including an address that would match, and
// requireGroupAdmin refuses everyone.
func TestMechanismInertWhenUnset(t *testing.T) {
	k := newSvc(t) // no WithBootstrapOwner
	b := makeOwnerless(t, k, "board")

	for _, caller := range []string{"ada@example.com", "anyone@example.com"} {
		if err := k.ClaimBoard(ctxAs(caller), b.ID); err != ErrForbidden {
			t.Errorf("ClaimBoard as %q with no bootstrap owner = %v, want ErrForbidden", caller, err)
		}
		role, err := k.RoleOn(context.Background(), identity.User{Email: caller}, b.ID)
		if err != nil || role != "" {
			t.Errorf("RoleOn as %q with no bootstrap owner = %q, %v, want \"\"", caller, role, err)
		}
	}
	if err := k.requireGroupAdmin(ctxAs("ada@example.com")); err != ErrForbidden {
		t.Errorf("requireGroupAdmin with no bootstrap owner = %v, want ErrForbidden", err)
	}
}

// listGrantsErrStore wraps a store so ListGrants returns a sentinel error, to
// prove boardHasOwner surfaces the error rather than reading it as "ownerless".
type listGrantsErrStore struct {
	store.Store
	err error
}

func (s listGrantsErrStore) ListGrants(context.Context, model.ID) ([]model.Grant, error) {
	return nil, s.err
}

// 12 (MUTANT). boardHasOwner surfaces a store error rather than reading it as
// "ownerless". The mutant — owned, _ := ListGrants(...) then treat !owned as
// reachable — turns a store blip into a privilege grant, so this must go red
// against it.
func TestBoardHasOwnerSurfacesStoreError(t *testing.T) {
	sentinel := errors.New("store on fire")
	st := listGrantsErrStore{Store: memory.New(), err: sentinel}
	k := New(st, WithClock(func() time.Time { return fixed }),
		WithIDs(func() model.ID { return "b1" }), WithBootstrapOwner("ada@example.com"))

	ada := identity.User{Email: "ada@example.com"}
	role, err := k.RoleOn(context.Background(), ada, model.ID("b1"))
	if !errors.Is(err, sentinel) {
		t.Errorf("RoleOn error = %v, want the store error surfaced", err)
	}
	if role == model.RoleOwner {
		t.Error("RoleOn returned owner on a board whose owner the store error was hiding")
	}
	if err := k.ClaimBoard(ctxAs("ada@example.com"), model.ID("b1")); !errors.Is(err, sentinel) {
		t.Errorf("ClaimBoard error = %v, want the store error surfaced", err)
	}

	// A missing board surfaces the store's ErrNotFound, not a claimable
	// ownerless board. memory.New() returns ErrNotFound for an absent board.
	plain := bootstrapSvc(t, "ada@example.com")
	if _, err := plain.RoleOn(context.Background(), ada, model.ID("nope")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RoleOn on a missing board = %v, want store.ErrNotFound", err)
	}
}

// 13. ämtli regression: enforcement, not the claim, is what refuses an
// anonymous caller. An anonymous ctx and a service token hold "" on every
// board, ownerless or owned, with the bootstrap owner set and unset, and a
// write is refused — until ämtli's principal is granted write, after which the
// same write succeeds.
func TestAmtliRegression(t *testing.T) {
	for _, set := range []bool{true, false} {
		var k *Byge
		if set {
			k = bootstrapSvc(t, "ada@example.com")
		} else {
			k = newSvc(t)
		}
		free := makeOwnerless(t, k, "free")
		owned := makeOwnerless(t, k, "owned")
		grant(t, k, owned.ID, model.UserPrincipal("bob@example.com"), model.RoleOwner)

		anon := context.Background()
		svcTok := identity.NewContext(context.Background(), identity.User{Service: "ämtli"})
		for _, b := range []*model.Board{free, owned} {
			for name, ctx := range map[string]context.Context{"anonymous": anon, "ämtli": svcTok} {
				u := identity.FromContext(ctx)
				if role, err := k.RoleOn(ctx, u, b.ID); err != nil || role != "" {
					t.Errorf("set=%v %s RoleOn(%s) = %q, %v, want \"\"", set, name, b.Slug, role, err)
				}
				if _, err := k.CreateCard(ctx, b.ID, free.Columns[0].ID, CardInput{Title: "x"}); err != ErrForbidden {
					t.Errorf("set=%v %s CreateCard(%s) = %v, want ErrForbidden", set, name, b.Slug, err)
				}
			}
		}

		// Grant ämtli's principal write on the ownerless board, then the same
		// write succeeds. ämtli's principal is an address its deployment gives
		// it; here a plain address stands in for it.
		grant(t, k, free.ID, model.UserPrincipal("ämtli@example.com"), model.RoleWrite)
		amtli := identity.NewContext(context.Background(), identity.User{Email: "ämtli@example.com"})
		if _, err := k.CreateCard(amtli, free.ID, free.Columns[0].ID, CardInput{Title: "ok"}); err != nil {
			t.Errorf("set=%v granted ämtli CreateCard = %v, want nil", set, err)
		}
	}
}
