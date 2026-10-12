package model

import (
	"fmt"
	"strings"
	"time"
)

// Role is what a principal may do with a board. The three are ordered, and
// Allows is the only place that order is written down.
type Role string

const (
	// RoleRead opens the board and reads its cards.
	RoleRead Role = "read"
	// RoleWrite adds, moves, edits and archives, and is everything a board is
	// normally used for.
	RoleWrite Role = "write"
	// RoleOwner also changes the board's settings, hands out roles, and
	// deletes the board. Separate from RoleWrite because moving a card and
	// deleting the board it is on are not the same risk.
	RoleOwner Role = "owner"
)

// rank orders the roles. A role that is not one of the three ranks below
// RoleRead, so an unknown value read out of a row allows nothing rather than
// everything.
func (r Role) rank() int {
	switch r {
	case RoleRead:
		return 1
	case RoleWrite:
		return 2
	case RoleOwner:
		return 3
	default:
		return 0
	}
}

// Valid reports whether r is one of the three roles.
func (r Role) Valid() bool { return r.rank() > 0 }

// Allows reports whether holding r is enough to do what need requires.
func (r Role) Allows(need Role) bool { return r.rank() >= need.rank() && need.rank() > 0 }

// Max returns whichever of the two roles allows more. It is how the roles a
// caller holds through several principals become the one role they have.
func (r Role) Max(other Role) Role {
	if other.rank() > r.rank() {
		return other
	}
	return r
}

// PrincipalKind says what a grant is addressed to, and what its ID means.
type PrincipalKind string

const (
	// PrincipalUser is one person, by the address the identity provider hands
	// over. The ID is that address, lowercased.
	PrincipalUser PrincipalKind = "user"
	// PrincipalGroup is a group kept in this database, by its ID.
	PrincipalGroup PrincipalKind = "group"
	// PrincipalIDP is a group name that arrives with the request, from an
	// Access claim or a proxy header. The ID is that name, lowercased, and
	// this database never sees who is in it.
	PrincipalIDP PrincipalKind = "idp"
)

// Principal is who a grant is for.
type Principal struct {
	Kind PrincipalKind
	ID   string
}

// UserPrincipal returns the principal for an address, lowercased.
func UserPrincipal(email string) Principal {
	return Principal{Kind: PrincipalUser, ID: strings.ToLower(strings.TrimSpace(email))}
}

// GroupPrincipal returns the principal for a group kept here.
func GroupPrincipal(id ID) Principal { return Principal{Kind: PrincipalGroup, ID: string(id)} }

// IDPPrincipal returns the principal for a group name the provider sends,
// lowercased.
func IDPPrincipal(name string) Principal {
	return Principal{Kind: PrincipalIDP, ID: strings.ToLower(strings.TrimSpace(name))}
}

// Valid reports whether the kind is one of the three and the ID is not empty.
func (p Principal) Valid() bool {
	switch p.Kind {
	case PrincipalUser, PrincipalGroup, PrincipalIDP:
		return p.ID != ""
	default:
		return false
	}
}

// String is "kind:id", which is what a log line and a map key want.
func (p Principal) String() string { return fmt.Sprintf("%s:%s", p.Kind, p.ID) }

// Grant is one principal's role on one board. There is at most one per board
// and principal, so granting twice replaces rather than adds.
type Grant struct {
	BoardID   ID
	Principal Principal
	Role      Role
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Group is a set of addresses under a name, kept here rather than at the
// identity provider. Members are lowercased addresses, sorted.
type Group struct {
	ID        ID
	Name      string
	Members   []string
	CreatedAt time.Time
	UpdatedAt time.Time
}
