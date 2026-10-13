// The board list, one board, and the three writes that make, remove or reshape
// one: create, delete and the layout switch.

package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"byge/internal/identity"
	"byge/internal/model"
	"byge/internal/service"
	"byge/internal/store"
)

// index is the front door. With exactly one board it redirects into it, because
// a list of one is a page nobody wants; with any other number it is the list.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	boards, err := s.svc.VisibleBoardList(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ownerless, err := s.svc.OwnerlessBoards(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The single-board redirect yields to an ownerless board: VisibleBoardList
	// is grant-only, so the bootstrap owner who holds one owned board while an
	// ownerless board waits would be redirected past the claim block. Redirect
	// only when there is one board and nothing to claim.
	if len(boards) == 1 && len(ownerless) == 0 {
		http.Redirect(w, r, "/b/"+boards[0].Slug, http.StatusSeeOther)
		return
	}
	s.render(w, s.pages["boards"], "layout", http.StatusOK, boardsPage{
		Title: "Byge", User: identity.FromContext(r.Context()),
		Boards: boards, Ownerless: ownerless,
	})
}

// boardList draws every board the caller may read and the form that makes
// another.
func (s *Server) boardList(w http.ResponseWriter, r *http.Request) {
	boards, err := s.svc.VisibleBoardList(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ownerless, err := s.svc.OwnerlessBoards(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, s.pages["boards"], "layout", http.StatusOK,
		boardsPage{Title: "Boards", User: identity.FromContext(r.Context()), Boards: boards, Ownerless: ownerless})
}

// navBoards is the list behind the board name in the app bar. A read that fails
// logs and returns nothing: a board that cannot name its neighbours is still a
// board worth drawing, and the switcher simply does not open. It carries only
// the boards the caller may read, so the switcher never names one they hold
// nothing on.
func (s *Server) navBoards(ctx context.Context) []model.Board {
	boards, err := s.svc.VisibleBoardList(ctx)
	if err != nil {
		s.log.Error("board switcher", "err", err)
		return nil
	}
	return boards
}

// createBoard makes a board from a name and goes to it. A name that is empty or
// already taken re-renders the list with the reason rather than redirecting, so
// the message arrives on the page that asked rather than on a fresh one.
func (s *Server) createBoard(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.CreateBoard(r.Context(), r.FormValue("name"), "", nil)
	if err != nil {
		var ve *service.ValidationError
		if errors.As(err, &ve) || errors.Is(err, store.ErrConflict) {
			boards, lerr := s.svc.VisibleBoardList(r.Context())
			if lerr != nil {
				s.fail(w, r, lerr)
				return
			}
			msg := "a board with that name already exists"
			if ve != nil {
				msg = ve.Error()
			}
			s.render(w, s.pages["boards"], "layout", http.StatusBadRequest, boardsPage{Title: "Byge", User: identity.FromContext(r.Context()), Boards: boards, Error: msg})
			return
		}
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/b/"+b.Slug, http.StatusSeeOther)
}

// deleteBoard removes a board and everything on it.
//
// The largest thing here with no way back. A card can be archived and restored,
// a column's cards can be moved out from under it, and deleting a card or a
// label is final but small; a board takes its cards, its columns, its labels
// and its comments with it. So the
// name has to be typed, which is the one guard that cannot be satisfied by a
// misplaced click, and the board being deleted is named in the confirmation so
// the name being typed is the one in front of you.
//
// Case and surrounding space are forgiven. It is a name read off a screen, not
// a password, and the page says "type its name" rather than "exactly" because
// that is what the comparison below actually asks for.
func (s *Server) deleteBoard(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), b.Name) {
		boards, lerr := s.svc.VisibleBoardList(r.Context())
		if lerr != nil {
			s.fail(w, r, lerr)
			return
		}
		s.render(w, s.pages["boards"], "layout", http.StatusBadRequest, boardsPage{
			Title: "Boards", User: identity.FromContext(r.Context()), Boards: boards,
			Error: "To delete " + b.Name + ", type its name.",
		})
		return
	}
	if err := s.svc.DeleteBoard(r.Context(), b.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	// /boards rather than /, because / redirects straight back into the one
	// board that is left and the person doing this is tidying up.
	http.Redirect(w, r, "/boards", http.StatusSeeOther)
}

// board draws one board: its columns, its cards, and the search results when
// the query string carries a search.
func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u := identity.FromContext(r.Context())
	// One call for the whole board rather than one per card. It covers
	// archived cards too, so a search result carries its badge as well.
	counts, err := s.svc.CommentCounts(r.Context(), b.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// The search lives on the board's own URL rather than a page of its own,
	// so a result set can be linked to and reloading keeps it.
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	if raw != "" {
		hits, err := s.svc.Search(r.Context(), b.ID, service.ParseQuery(raw))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page := s.boardPage(u, b, nil, counts)
		page.Query, page.Searching = raw, true
		// The hits are a subset, and a quick edit on a result should offer the
		// same people as one on the board, so the list comes from the board.
		people := s.people(r, u, b.ID)
		clock := b.SLA.Clock()
		for _, c := range hits {
			page.Results = append(page.Results, s.cardView(u, b, clock, c, counts[c.ID], people))
		}
		page.Boards = s.navBoards(r.Context())
		s.render(w, s.pages["board"], "layout", http.StatusOK, page)
		return
	}

	cards, err := s.svc.Cards(r.Context(), b.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page := s.boardPage(u, b, cards, counts)
	page.Boards = s.navBoards(r.Context())
	s.render(w, s.pages["board"], "layout", http.StatusOK, page)
}

// setLayout switches the board between columns and rows. It posts from the
// board itself rather than the settings page: it is a thing you decide while
// looking at the board, and the answer is visible the moment you land back on
// it.
func (s *Server) setLayout(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.SetBoardLayout(r.Context(), b.ID, r.FormValue("layout")); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/b/"+b.Slug, http.StatusSeeOther)
}

// claimBoard makes the bootstrap owner the owner of an ownerless board. The
// board is resolved through s.svc.Board, which runs require(RoleRead): an
// arbitrary caller cannot read an ownerless board (RoleOn returns ""), so the
// 404/403 for an unknown slug or an unreadable board comes from there, before
// ClaimBoard. The bootstrap owner passes that gate on an ownerless board
// through the RoleOn shortcut, and ClaimBoard then writes the owner grant.
func (s *Server) claimBoard(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.Board(r.Context(), r.PathValue("board"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.ClaimBoard(r.Context(), b.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/b/"+b.Slug, http.StatusSeeOther)
}
