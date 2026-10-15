// Board grants: who may read, write and own a board. Every method here is
// owner-only, through require(ctx, boardID, RoleOwner) as its first act, so a
// reader or a stranger is refused in the service and not in a handler. This is
// the stage-3 rule applied to the one page that hands out roles.

package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/mail"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/store"
)

// What a refused grant says. Kept as constants because two methods refuse the
// same change and the page's tests match the text.
const (
	msgLastOwner    = "a board must keep an owner; make someone else owner first"
	msgNoOwnerYet   = "a board must keep an owner; this one has none yet, so claim it or add an owner first"
	msgNotAnAddress = "not an address; a user is the address they sign in with, such as ada@example.com"
)

// BoardGrants lists a board's grants, each a principal and its role. Owner
// only: the page that lists who may do what is itself one of the things only
// an owner may see.
func (k *Byge) BoardGrants(ctx context.Context, boardID model.ID) ([]model.Grant, error) {
	if err := k.require(ctx, boardID, model.RoleOwner); err != nil {
		return nil, err
	}
	return k.store.ListGrants(ctx, boardID)
}

// SetBoardGrant writes one principal's role on a board, replacing whatever that
// principal held. Owner only.
//
// The principal has to be one some caller can present under the auth mode this
// process runs in (checkPrincipal), or the grant is stored and matches nobody:
// that is how "sapm" became a user grant. A principal built by hand is put into
// the form grants are matched in first, so it gets the same result as one built
// through the model constructors.
//
// A grant to a local group names a group this database keeps, so the group has
// to exist. The store refuses an unknown group with ErrNotFound, which a
// handler would turn into a 404 that reads as "the board is gone"; this checks
// the group first and turns the miss into a validation error, so the add form
// re-renders at 400 with the reason beside the field.
//
// The last owner cannot be demoted away: a board that exists must have an
// owner, or it is visible to nobody and fixable only in the database.
func (k *Byge) SetBoardGrant(ctx context.Context, boardID model.ID, p model.Principal, role model.Role) error {
	if err := k.require(ctx, boardID, model.RoleOwner); err != nil {
		return err
	}
	// On the raw value: lowercasing turns a byte that is not UTF-8 into U+FFFD,
	// after which the storable check has nothing left to refuse.
	if err := CheckStorable("id", p.ID); err != nil {
		return err
	}
	p = normalPrincipal(p)
	if err := k.checkPrincipal(p); err != nil {
		return err
	}
	if !role.Valid() {
		return invalid("role", "must be read, write or owner")
	}
	if p.Kind == model.PrincipalGroup {
		if _, err := k.store.GetGroup(ctx, model.ID(p.ID)); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return invalid("id", "no such local group")
			}
			return err
		}
	}
	grants, err := k.store.ListGrants(ctx, boardID)
	if err != nil {
		return err
	}
	if err := keepsAnOwner(grants, p, role); err != nil {
		return err
	}
	return k.store.SetGrant(ctx, &model.Grant{BoardID: boardID, Principal: p, Role: role})
}

// GrantHints is what the access page's add forms offer: principals the server
// already knows that SetBoardGrant would accept and that hold no grant on this
// board yet, sorted and unique.
type GrantHints struct {
	Users         []string // lowercased addresses, or logins in proxy mode
	IDPGroups     []string // lowercased idp group names
	ProxyIdentity bool     // the rules in force are the proxy-mode ones
}

// BoardGrantHints collects suggestions for the add forms on boardID's access
// page. Owner only. users and idpGroups are candidates the caller knows from
// configuration; they go through the same filter as everything else.
//
// The sources are what this caller can already see: grants on the other boards
// they own, the assignees on this board's cards and their own idp groups. A
// principal already granted here is left out, because its row is where it is
// changed, and so is anything SetBoardGrant would refuse, so a suggestion is
// never one the page then turns down.
func (k *Byge) BoardGrantHints(ctx context.Context, boardID model.ID, users, idpGroups []string) (GrantHints, error) {
	if err := k.require(ctx, boardID, model.RoleOwner); err != nil {
		return GrantHints{}, err
	}
	knownUsers, knownIDP, err := k.hintCandidates(ctx, boardID, identity.FromContext(ctx))
	if err != nil {
		return GrantHints{}, err
	}
	here, err := k.store.ListGrants(ctx, boardID)
	if err != nil {
		return GrantHints{}, err
	}
	taken := make(map[model.Principal]bool, len(here))
	for _, g := range here {
		taken[g.Principal] = true
	}
	return GrantHints{
		Users:         suggestible(append(knownUsers, users...), model.UserPrincipal, checkUserID, k.proxyIdentity, taken),
		IDPGroups:     suggestible(append(knownIDP, idpGroups...), model.IDPPrincipal, checkIDPName, k.proxyIdentity, taken),
		ProxyIdentity: k.proxyIdentity,
	}, nil
}

// hintCandidates gathers what BoardGrantHints filters: the user and idp
// principals granted on the other boards the caller owns, this board's
// assignees, and the caller's own idp groups.
func (k *Byge) hintCandidates(ctx context.Context, boardID model.ID, u identity.User) (users, idps []string, err error) {
	roles, err := k.VisibleBoards(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	// Sorted so the store is read in the same order on every view.
	for _, id := range slices.Sorted(maps.Keys(roles)) {
		if id == boardID || roles[id] != model.RoleOwner {
			continue
		}
		grants, err := k.store.ListGrants(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		for _, g := range grants {
			switch g.Principal.Kind {
			case model.PrincipalUser:
				users = append(users, g.Principal.ID)
			case model.PrincipalIDP:
				idps = append(idps, g.Principal.ID)
			}
			// A local group is picked from the group list, not suggested.
		}
	}
	cards, err := k.store.ListCards(ctx, boardID)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range cards {
		users = append(users, c.Assignee)
	}
	return users, append(idps, u.Groups...), nil
}

// suggestible keeps the candidates that, put into principal form, pass check
// and hold no grant on this board yet. Sorted and unique.
func suggestible(candidates []string, principal func(string) model.Principal,
	check func(string, bool) error, proxy bool, taken map[model.Principal]bool) []string {
	keep := map[string]bool{}
	for _, s := range candidates {
		p := principal(s)
		if !taken[p] && check(p.ID, proxy) == nil {
			keep[p.ID] = true
		}
	}
	return slices.Sorted(maps.Keys(keep))
}

// normalPrincipal puts a principal into the form grants are matched in, so a
// caller that builds one by hand gets the same result as the constructors.
func normalPrincipal(p model.Principal) model.Principal {
	switch p.Kind {
	case model.PrincipalUser:
		return model.UserPrincipal(p.ID)
	case model.PrincipalIDP:
		return model.IDPPrincipal(p.ID)
	}
	return p
}

// checkPrincipal refuses a principal that no caller can ever present under the
// auth mode this process runs in.
func (k *Byge) checkPrincipal(p model.Principal) error {
	switch p.Kind {
	case model.PrincipalUser:
		return checkUserID(p.ID, k.proxyIdentity)
	case model.PrincipalIDP:
		return checkIDPName(p.ID, k.proxyIdentity)
	case model.PrincipalGroup:
		if err := CheckStorable("id", p.ID); err != nil {
			return err
		}
		if p.ID == "" {
			return invalid("id", "choose a local group")
		}
		return nil // whether it exists is checked after the role
	default:
		return invalid("kind", "unknown kind")
	}
}

// checkUserID refuses a user grant no caller can present. With a verified
// assertion (access mode) the caller is the email claim Cloudflare signs, so a
// user is an address and nothing else. Behind a proxy (proxy mode) the caller
// is whatever the header carries, which may be a login such as u236858, so any
// token is allowed; internal whitespace is still refused, because a space in an
// address or a login is almost always a typo, and a proxy that sends display
// names cannot be granted from this page.
//
// id is expected lowercased and trimmed, as UserPrincipal leaves it.
func checkUserID(id string, proxy bool) error {
	if err := CheckStorable("id", id); err != nil {
		return err
	}
	if id == "" {
		if proxy {
			return invalid("id", "enter an address or login")
		}
		return invalid("id", "enter an address")
	}
	if utf8.RuneCountInString(id) > MaxPrincipal {
		return invalid("id", fmt.Sprintf("must be at most %d characters", MaxPrincipal))
	}
	if strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		if proxy {
			return invalid("id", "a login has no spaces in it")
		}
		return invalid("id", "an address has no spaces in it")
	}
	if proxy {
		return nil
	}
	// ParseAddress alone also takes "Ada <ada@example.com>" and a quoted local
	// part; requiring it to hand back exactly what was typed refuses both, and
	// the count refuses a second @ before the parser has to.
	local, domain, _ := strings.Cut(id, "@")
	if strings.Count(id, "@") != 1 || local == "" || domain == "" {
		return invalid("id", msgNotAnAddress)
	}
	if a, err := mail.ParseAddress(id); err != nil || a.Address != id {
		return invalid("id", msgNotAnAddress)
	}
	return nil
}

// checkIDPName refuses an idp group name no caller can present. The signed
// claim is a JSON list and is never split, so in access mode a name may hold a
// comma and inner spaces the way Okta and Entra groups do. A proxy sends one
// comma-separated header, so there a name with a comma can never arrive.
//
// name is expected lowercased and trimmed, as IDPPrincipal leaves it.
func checkIDPName(name string, proxy bool) error {
	if err := CheckStorable("id", name); err != nil {
		return err
	}
	if name == "" {
		return invalid("id", "enter a group name")
	}
	if utf8.RuneCountInString(name) > MaxPrincipal {
		return invalid("id", fmt.Sprintf("must be at most %d characters", MaxPrincipal))
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return invalid("id", "a group name is one line of text")
	}
	if proxy && strings.Contains(name, ",") {
		return invalid("id", "a group name cannot contain a comma; the proxy sends groups as one comma-separated list")
	}
	return nil
}

// keepsAnOwner refuses a change that leaves the board with no owner grant.
// role is p's role after the change, "" when p is removed. A non-last owner may
// be demoted or removed, self included, as long as another owner remains.
//
// The count comes from a ListGrants read and the write follows without a lock,
// so two owners demoting or removing each other at the same moment can both
// pass and leave the board ownerless. The bootstrap owner's Claim is the way
// back. Closing it needs a conditional write in all three stores.
func keepsAnOwner(grants []model.Grant, p model.Principal, role model.Role) error {
	before, after := 0, 0
	for _, g := range grants {
		if g.Role == model.RoleOwner {
			before++
			if g.Principal != p {
				after++
			}
		}
	}
	if role == model.RoleOwner {
		after++
	}
	switch {
	case after > 0:
		return nil
	case before == 0:
		return invalid("role", msgNoOwnerYet)
	default:
		return invalid("role", msgLastOwner)
	}
}

// ClaimBoard makes the bootstrap owner the owner of a board that has no owner.
// It is how an ownerless board — the default board EnsureDefaultBoard writes
// with no grant, or any board left ownerless — gets its first owner, and it is
// the only way to do so.
//
// Only the configured AUTH_BOOTSTRAP_OWNER may call it, enforced here and not in
// a handler, consistent with every other grant method. It refuses a board that
// already has an owner: claiming is for the ownerless, and a board with an owner
// is changed through SetBoardGrant by that owner, not taken over from config.
//
// The check-then-write (the ListGrants scan, then SetGrant) is not atomic:
// nothing holds a lock between the two. It is safe rather than a second
// uncontrolled owner, because every writer that reaches here sets the identical
// user:<bootstrap> → owner grant and SetGrant replaces rather than appends on
// the principal key, so the worst case is a redundant write of the same grant;
// SetBoardGrant's last-owner guard then governs the board from the first write.
func (k *Byge) ClaimBoard(ctx context.Context, boardID model.ID) error {
	if !k.isBootstrapOwner(ctx) {
		return ErrForbidden
	}
	grants, err := k.store.ListGrants(ctx, boardID)
	if err != nil {
		return err
	}
	for _, g := range grants {
		if g.Role == model.RoleOwner {
			// Already owned. The bootstrap owner is not an owner of it unless a
			// grant says so, so this is refused rather than silently adding a
			// second owner from config.
			return ErrForbidden
		}
	}
	return k.store.SetGrant(ctx, &model.Grant{
		BoardID:   boardID,
		Principal: model.UserPrincipal(identity.FromContext(ctx).Email),
		Role:      model.RoleOwner,
	})
}

// RemoveBoardGrant takes a principal off a board. Owner only, and it will not
// remove the last owner for the same reason SetBoardGrant will not demote one.
//
// It checks no format and renormalises nothing, so a grant stored before the
// rules in checkPrincipal existed can always be taken off again.
func (k *Byge) RemoveBoardGrant(ctx context.Context, boardID model.ID, p model.Principal) error {
	if err := k.require(ctx, boardID, model.RoleOwner); err != nil {
		return err
	}
	grants, err := k.store.ListGrants(ctx, boardID)
	if err != nil {
		return err
	}
	if err := keepsAnOwner(grants, p, ""); err != nil {
		return err
	}
	return k.store.DeleteGrant(ctx, boardID, p)
}
