package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"byge/internal/model"
	"byge/internal/store"
)

// querier is the part of *sql.DB and *sql.Tx these helpers use, so a read can
// run inside a transaction or on its own without being written twice.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readGroups reads the groups matching where, with their members, in one pass
// per table. Two queries rather than a join, because a join repeats the group
// row once per member and the assembly costs the same either way.
func (s *Store) readGroups(ctx context.Context, q querier, where string, args ...any) ([]model.Group, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, created_at, updated_at FROM groups `+where, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []model.Group
	byID := map[model.ID]int{}
	for rows.Next() {
		var g model.Group
		var created, updated string
		if err := rows.Scan(&g.ID, &g.Name, &created, &updated); err != nil {
			return nil, mapErr(err)
		}
		if g.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if g.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		byID[g.ID] = len(out)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]any, 0, len(out))
	for i := range out {
		ids = append(ids, out[i].ID)
	}
	mrows, err := q.QueryContext(ctx,
		`SELECT group_id, email FROM group_members WHERE group_id IN `+inClause(len(ids))+` ORDER BY email`, ids...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = mrows.Close() }()
	for mrows.Next() {
		var gid model.ID
		var email string
		if err := mrows.Scan(&gid, &email); err != nil {
			return nil, mapErr(err)
		}
		if i, ok := byID[gid]; ok {
			out[i].Members = append(out[i].Members, email)
		}
	}
	return out, mapErr(mrows.Err())
}

// ListGroups returns every group with its members, by name.
func (s *Store) ListGroups(ctx context.Context) ([]model.Group, error) {
	out, err := s.readGroups(ctx, s.db, `ORDER BY lower(name), id`)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []model.Group{}
	}
	return out, nil
}

// GetGroup returns one group with its members.
func (s *Store) GetGroup(ctx context.Context, id model.ID) (*model.Group, error) {
	out, err := s.readGroups(ctx, s.db, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, store.ErrNotFound
	}
	return &out[0], nil
}

// writeMembers replaces a group's membership. The caller holds the
// transaction, so a group is never briefly empty to another reader.
func writeMembers(ctx context.Context, tx *sql.Tx, id model.ID, members []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_members WHERE group_id = ?`, id); err != nil {
		return mapErr(err)
	}
	seen := map[string]bool{}
	for _, m := range members {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_members (group_id, email) VALUES (?, ?)`, id, m); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// CreateGroup writes the group and its members.
func (s *Store) CreateGroup(ctx context.Context, g *model.Group) error {
	if g.ID == "" {
		return store.ErrInvalid
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO groups (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			g.ID, g.Name, timeArg(g.CreatedAt), timeArg(g.UpdatedAt))
		if err != nil {
			return mapErr(err)
		}
		return writeMembers(ctx, tx, g.ID, g.Members)
	})
}

// UpdateGroup writes the name and replaces the whole membership.
func (s *Store) UpdateGroup(ctx context.Context, g *model.Group) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := affected(tx.ExecContext(ctx,
			`UPDATE groups SET name = ?, updated_at = ? WHERE id = ?`, g.Name, timeArg(g.UpdatedAt), g.ID)); err != nil {
			return err
		}
		return writeMembers(ctx, tx, g.ID, g.Members)
	})
}

// DeleteGroup removes the group, its membership and every grant made to it.
// The members go with the foreign key; the grants cannot, because
// principal_id holds an address, a group ID or a provider group name
// depending on the kind beside it.
func (s *Store) DeleteGroup(ctx context.Context, id model.ID) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM board_grants WHERE principal_kind = ? AND principal_id = ?`, model.PrincipalGroup, string(id)); err != nil {
			return mapErr(err)
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id))
	})
}

// boardExists reports whether the board is there, so a grant can answer
// ErrNotFound for the board rather than for whatever the insert hit.
func boardExists(ctx context.Context, q querier, id model.ID) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM boards WHERE id = ?`, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, mapErr(err)
}

// ListGrants returns a board's grants, by principal.
func (s *Store) ListGrants(ctx context.Context, boardID model.ID) ([]model.Grant, error) {
	ok, err := boardExists(ctx, s.db, boardID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, store.ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT principal_kind, principal_id, role, created_at, updated_at FROM board_grants
		 WHERE board_id = ? ORDER BY principal_kind, principal_id`, boardID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	out := []model.Grant{}
	for rows.Next() {
		g := model.Grant{BoardID: boardID}
		var created, updated string
		if err := rows.Scan(&g.Principal.Kind, &g.Principal.ID, &g.Role, &created, &updated); err != nil {
			return nil, mapErr(err)
		}
		if g.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if g.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, mapErr(rows.Err())
}

// SetGrant writes one principal's role on one board, replacing whatever role
// that principal held.
func (s *Store) SetGrant(ctx context.Context, g *model.Grant) error {
	if !g.Principal.Valid() || !g.Role.Valid() {
		return store.ErrInvalid
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		ok, err := boardExists(ctx, tx, g.BoardID)
		if err != nil {
			return err
		}
		if !ok {
			return store.ErrNotFound
		}
		if g.Principal.Kind == model.PrincipalGroup {
			var one int
			switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM groups WHERE id = ?`, g.Principal.ID).Scan(&one); {
			case err == sql.ErrNoRows:
				return store.ErrNotFound
			case err != nil:
				return mapErr(err)
			}
		}
		// The created_at of a grant that is being replaced is when the
		// principal first got a role on this board, which is worth keeping;
		// only the role and updated_at move.
		_, err = tx.ExecContext(ctx,
			`INSERT INTO board_grants (board_id, principal_kind, principal_id, role, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT (board_id, principal_kind, principal_id)
			 DO UPDATE SET role = excluded.role, updated_at = excluded.updated_at`,
			g.BoardID, g.Principal.Kind, g.Principal.ID, g.Role, timeArg(g.CreatedAt), timeArg(g.UpdatedAt))
		return mapErr(err)
	})
}

// DeleteGrant takes a principal off a board. Taking off a grant that is not
// there is not an error: the caller wanted the principal to hold nothing and
// it holds nothing.
func (s *Store) DeleteGrant(ctx context.Context, boardID model.ID, p model.Principal) error {
	ok, err := boardExists(ctx, s.db, boardID)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM board_grants WHERE board_id = ? AND principal_kind = ? AND principal_id = ?`,
		boardID, p.Kind, p.ID)
	return mapErr(err)
}

// BoardRoles returns the role each of these principals holds, per board,
// reduced to one role per board.
func (s *Store) BoardRoles(ctx context.Context, principals []model.Principal) (map[model.ID]model.Role, error) {
	out := map[model.ID]model.Role{}
	args := make([]any, 0, len(principals)*2)
	var terms []string
	for _, p := range principals {
		if !p.Valid() {
			continue
		}
		terms = append(terms, `(principal_kind = ? AND principal_id = ?)`)
		args = append(args, p.Kind, p.ID)
	}
	// No principals is no roles, and here that is load-bearing: with no terms
	// the WHERE below is empty and an empty WHERE is every board, which is
	// the opposite answer. Removing this line turns an unidentified caller
	// into a caller who may see everything.
	if len(terms) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT board_id, role FROM board_grants WHERE `+strings.Join(terms, " OR "), args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var board model.ID
		var role model.Role
		if err := rows.Scan(&board, &role); err != nil {
			return nil, mapErr(err)
		}
		if role.Valid() {
			out[board] = out[board].Max(role)
		}
	}
	return out, mapErr(rows.Err())
}

// GroupsOf returns the IDs of the groups this address is a member of.
func (s *Store) GroupsOf(ctx context.Context, email string) ([]model.ID, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT group_id FROM group_members WHERE email = ? ORDER BY group_id`, email)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []model.ID
	for rows.Next() {
		var id model.ID
		if err := rows.Scan(&id); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, id)
	}
	return out, mapErr(rows.Err())
}
