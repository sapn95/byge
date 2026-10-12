package memory

import (
	"context"
	"sort"
	"strings"

	"byge/internal/model"
	"byge/internal/store"
)

// normalMembers returns the members as the store keeps them: lowercased,
// trimmed, sorted and without duplicates or blanks. The same list typed twice
// with different spacing has to come back the same, or a membership check
// would depend on how somebody filled in a form.
func normalMembers(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, m := range in {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// copyGroup hands out a group whose slice the caller shares with nobody, for
// the reason copyBoard gives.
func copyGroup(g *model.Group) *model.Group {
	c := *g
	c.Members = append([]string(nil), g.Members...)
	return &c
}

// nameTaken reports whether another group already answers to this name,
// compared without case, the way the unique index on the SQL backends does.
func (s *Store) nameTaken(name string, except model.ID) bool {
	lowered := strings.ToLower(strings.TrimSpace(name))
	for id, g := range s.groups {
		if id != except && strings.ToLower(strings.TrimSpace(g.Name)) == lowered {
			return true
		}
	}
	return false
}

// ListGroups returns every group with its members, by name.
func (s *Store) ListGroups(context.Context) ([]model.Group, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Group, 0, len(s.groups))
	for _, g := range s.groups {
		out = append(out, *copyGroup(g))
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetGroup returns one group with its members.
func (s *Store) GetGroup(_ context.Context, id model.ID) (*model.Group, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.groups[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyGroup(g), nil
}

// CreateGroup writes the group and its members.
func (s *Store) CreateGroup(_ context.Context, g *model.Group) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.ID == "" {
		return store.ErrInvalid
	}
	if _, exists := s.groups[g.ID]; exists || s.nameTaken(g.Name, g.ID) {
		return store.ErrConflict
	}
	stored := *g
	stored.Members = normalMembers(g.Members)
	s.groups[g.ID] = &stored
	return nil
}

// UpdateGroup writes the name and replaces the whole membership.
func (s *Store) UpdateGroup(_ context.Context, g *model.Group) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.groups[g.ID]
	if !ok {
		return store.ErrNotFound
	}
	if s.nameTaken(g.Name, g.ID) {
		return store.ErrConflict
	}
	stored := *g
	stored.CreatedAt = old.CreatedAt
	stored.Members = normalMembers(g.Members)
	s.groups[g.ID] = &stored
	return nil
}

// DeleteGroup removes the group and every grant made to it. There is no
// foreign key doing that on the SQL backends and there cannot be, because
// principal_id holds an address, a group ID or a provider group name
// depending on the kind beside it, so all three delete the grants by hand. A
// grant left pointing at a group that is gone is a row nobody can see and
// nobody can revoke.
func (s *Store) DeleteGroup(_ context.Context, id model.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.groups, id)
	gone := model.GroupPrincipal(id)
	for board, byPrincipal := range s.grants {
		delete(byPrincipal, gone)
		if len(byPrincipal) == 0 {
			delete(s.grants, board)
		}
	}
	return nil
}

// ListGrants returns a board's grants, by principal.
func (s *Store) ListGrants(_ context.Context, boardID model.ID) ([]model.Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.boards[boardID]; !ok {
		return nil, store.ErrNotFound
	}
	out := make([]model.Grant, 0, len(s.grants[boardID]))
	for _, g := range s.grants[boardID] {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Principal.String() < out[j].Principal.String()
	})
	return out, nil
}

// SetGrant writes one principal's role on one board, replacing whatever role
// that principal held.
func (s *Store) SetGrant(_ context.Context, g *model.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !g.Principal.Valid() || !g.Role.Valid() {
		return store.ErrInvalid
	}
	if _, ok := s.boards[g.BoardID]; !ok {
		return store.ErrNotFound
	}
	if g.Principal.Kind == model.PrincipalGroup {
		if _, ok := s.groups[model.ID(g.Principal.ID)]; !ok {
			return store.ErrNotFound
		}
	}
	if s.grants[g.BoardID] == nil {
		s.grants[g.BoardID] = map[model.Principal]*model.Grant{}
	}
	stored := *g
	if old, ok := s.grants[g.BoardID][g.Principal]; ok {
		stored.CreatedAt = old.CreatedAt
	}
	s.grants[g.BoardID][g.Principal] = &stored
	return nil
}

// DeleteGrant takes a principal off a board. Taking off a grant that is not
// there is not an error.
func (s *Store) DeleteGrant(_ context.Context, boardID model.ID, p model.Principal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.boards[boardID]; !ok {
		return store.ErrNotFound
	}
	delete(s.grants[boardID], p)
	if len(s.grants[boardID]) == 0 {
		delete(s.grants, boardID)
	}
	return nil
}

// BoardRoles returns the role each of these principals holds, per board,
// reduced to one role per board.
func (s *Store) BoardRoles(_ context.Context, principals []model.Principal) (map[model.ID]model.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[model.ID]model.Role{}
	// An early return, not the thing that makes an empty set safe: the loop
	// below matches against wanted, which is empty too, so it would answer
	// the same without this. The SQL backends are where the empty set has to
	// be caught, because there an unrestricted query is every board.
	if len(principals) == 0 {
		return out, nil
	}
	wanted := make(map[model.Principal]bool, len(principals))
	for _, p := range principals {
		if p.Valid() {
			wanted[p] = true
		}
	}
	for board, byPrincipal := range s.grants {
		for p, g := range byPrincipal {
			if !wanted[p] || !g.Role.Valid() {
				continue
			}
			out[board] = out[board].Max(g.Role)
		}
	}
	return out, nil
}

// GroupsOf returns the IDs of the groups this address is a member of.
func (s *Store) GroupsOf(_ context.Context, email string) ([]model.ID, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.ID
	for id, g := range s.groups {
		for _, m := range g.Members {
			if m == email {
				out = append(out, id)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
