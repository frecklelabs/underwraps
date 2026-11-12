package data

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) access(ctx context.Context, v Identity, id int64) (List, int64, error) {
	uid, err := s.user(ctx, v)
	if err != nil {
		return List{}, 0, err
	}
	var l List
	err = s.db.QueryRowContext(ctx, `SELECT id,owner_id,name,occasion,created,updated,revision FROM lists l WHERE id=? AND (owner_id=? OR EXISTS(SELECT 1 FROM shares sh JOIN groups g ON g.id=sh.group_id AND g.disabled=0 JOIN memberships m ON m.group_id=g.id AND m.active=1 WHERE sh.list_id=l.id AND sh.active=1 AND m.user_id=?))`, id, uid, uid).Scan(&l.ID, &l.OwnerID, &l.Name, &l.Occasion, &l.CreatedAt, &l.UpdatedAt, &l.Revision)
	if err == sql.ErrNoRows {
		return List{}, 0, ErrMissing
	}
	l.Archived = s.archived(l.Occasion)
	return l, uid, err
}
func (s *Store) writable(ctx context.Context, v Identity, id int64, owner bool) (List, int64, error) {
	l, u, err := s.access(ctx, v, id)
	if err != nil {
		return l, u, err
	}
	if owner && u != l.OwnerID {
		return List{}, 0, ErrMissing
	}
	if l.Archived {
		return List{}, 0, ErrArchived
	}
	return l, u, nil
}
func validList(name, date string) bool {
	_, err := time.Parse("2006-01-02", date)
	return name != "" && err == nil
}
func (s *Store) allowedGroups(ctx context.Context, uid int64, groups []int64) error {
	if len(groups) == 0 {
		return ErrInvalid
	}
	seen := map[int64]bool{}
	for _, g := range groups {
		if seen[g] {
			return ErrInvalid
		}
		seen[g] = true
		var n int
		err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM memberships m JOIN groups g ON g.id=m.group_id WHERE m.user_id=? AND m.group_id=? AND m.active=1 AND g.disabled=0`, uid, g).Scan(&n)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrMissing
		}
	}
	return nil
}
func (s *Store) CreateList(ctx context.Context, v Identity, name, date string, groups []int64) (List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	uid, err := s.user(ctx, v)
	if err != nil {
		return List{}, err
	}
	if !validList(name, date) {
		return List{}, ErrInvalid
	}
	if s.archived(date) {
		return List{}, ErrArchived
	}
	if err = s.allowedGroups(ctx, uid, groups); err != nil {
		return List{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return List{}, err
	}
	defer tx.Rollback()
	now := s.now()
	r, err := tx.ExecContext(ctx, "INSERT INTO lists(owner_id,name,occasion,created,updated) VALUES(?,?,?,?,?)", uid, name, date, now, now)
	if err != nil {
		return List{}, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return List{}, err
	}
	for _, g := range groups {
		if _, err = tx.ExecContext(ctx, "INSERT INTO shares VALUES(?,?,1)", id, g); err != nil {
			return List{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return List{}, err
	}
	return List{ID: id, OwnerID: uid, Name: name, Occasion: date, CreatedAt: now, UpdatedAt: now, Revision: 1}, nil
}
func (s *Store) UpdateList(ctx context.Context, v Identity, id int64, name, date string, groups []int64) (List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, uid, err := s.writable(ctx, v, id, true)
	if err != nil {
		return List{}, err
	}
	if !validList(name, date) {
		return List{}, ErrInvalid
	}
	if s.archived(date) {
		return List{}, ErrArchived
	}
	if err = s.allowedGroups(ctx, uid, groups); err != nil {
		return List{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return List{}, err
	}
	defer tx.Rollback()
	now := s.now()
	if _, err = tx.ExecContext(ctx, "UPDATE lists SET name=?,occasion=?,updated=?,revision=revision+1 WHERE id=?", name, date, now, id); err != nil {
		return List{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE shares SET active=0 WHERE list_id=?", id); err != nil {
		return List{}, err
	}
	for _, g := range groups {
		if _, err = tx.ExecContext(ctx, "INSERT INTO shares VALUES(?,?,1) ON CONFLICT(list_id,group_id) DO UPDATE SET active=1", id, g); err != nil {
			return List{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return List{}, err
	}
	l.Name = name
	l.Occasion = date
	l.UpdatedAt = now
	l.Revision++
	return l, nil
}
func validPage(p Page) bool { return p.Offset >= 0 && p.Limit > 0 && p.Limit <= 100 }

// ListDetail selects the projection before querying any items. Pagination and counts
// use the same predicate, so hidden activity cannot shift an owner's pages.
func (s *Store) ListDetail(ctx context.Context, v Identity, id int64, p Page) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detail(ctx, v, id, p, false)
}
func (s *Store) DeletedItems(ctx context.Context, v Identity, id int64, p Page) (OwnerView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, u, err := s.access(ctx, v, id)
	if err != nil {
		return OwnerView{}, err
	}
	if l.OwnerID != u {
		return OwnerView{}, ErrMissing
	}
	view, err := s.detail(ctx, v, id, p, true)
	if err != nil {
		return OwnerView{}, err
	}
	return *view.Owner, nil
}
func (s *Store) detail(ctx context.Context, v Identity, id int64, p Page, deleted bool) (View, error) {
	l, uid, err := s.access(ctx, v, id)
	if err != nil {
		return View{}, err
	}
	if !validPage(p) {
		return View{}, ErrInvalid
	}
	if uid == l.OwnerID {
		out := OwnerView{List: l, Items: []OwnerItem{}, Offset: p.Offset, Limit: p.Limit}
		where := "list_id=? AND surprise=0 AND deleted=?"
		if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE "+where, id, deleted).Scan(&out.Count); err != nil {
			return View{}, err
		}
		rows, err := s.db.QueryContext(ctx, "SELECT id,name,link,price,note,image,quantity,priority,created,updated,revision,deleted FROM items WHERE "+where+" ORDER BY priority DESC,id ASC LIMIT ? OFFSET ?", id, deleted, p.Limit, p.Offset)
		if err != nil {
			return View{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var i OwnerItem
			if err = rows.Scan(&i.ID, &i.Name, &i.Link, &i.Price, &i.Note, &i.Image, &i.Quantity, &i.Priority, &i.CreatedAt, &i.UpdatedAt, &i.Revision, &i.Deleted); err != nil {
				return View{}, err
			}
			out.Items = append(out.Items, i)
		}
		return View{Owner: &out}, rows.Err()
	}
	out := MemberView{List: l, Items: []ViewerItem{}, Offset: p.Offset, Limit: p.Limit}
	where := "i.list_id=? AND (i.deleted=0 OR (i.surprise=0 AND c.buyer_id IS NOT NULL))"
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM items i LEFT JOIN claims c ON c.item_id=i.id AND c.active=1 WHERE "+where, id).Scan(&out.Count); err != nil {
		return View{}, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT i.id,i.name,i.link,i.price,i.note,i.image,i.quantity,i.priority,i.created,i.updated,i.revision,i.deleted,i.surprise,c.buyer_id,i.creator_id,COALESCE(u.name,'') FROM items i LEFT JOIN claims c ON c.item_id=i.id AND c.active=1 LEFT JOIN users u ON u.id=c.buyer_id WHERE "+where+" ORDER BY i.priority DESC,i.id ASC LIMIT ? OFFSET ?", id, p.Limit, p.Offset)
	if err != nil {
		return View{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var i ViewerItem
		if err = rows.Scan(&i.ID, &i.Name, &i.Link, &i.Price, &i.Note, &i.Image, &i.Quantity, &i.Priority, &i.CreatedAt, &i.UpdatedAt, &i.Revision, &i.Deleted, &i.Surprise, &i.BuyerID, &i.CreatorID, &i.BuyerName); err != nil {
			return View{}, err
		}
		i.RemovedByOwner = i.Deleted && !i.Surprise
		i.Surprise = i.Surprise || i.RemovedByOwner
		out.Items = append(out.Items, i)
	}
	return View{Member: &out}, rows.Err()
}
func (s *Store) Lists(ctx context.Context, v Identity, archived bool, p Page) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	uid, err := s.user(ctx, v)
	if err != nil {
		return nil, err
	}
	if !validPage(p) {
		return nil, ErrInvalid
	}
	// Discovery IDs and ordering depend only on owner-visible list fields.
	rows, err := s.db.QueryContext(ctx, `SELECT l.id FROM lists l WHERE (l.owner_id=? OR EXISTS(SELECT 1 FROM shares sh JOIN groups g ON g.id=sh.group_id AND g.disabled=0 JOIN memberships m ON m.group_id=g.id AND m.active=1 WHERE sh.list_id=l.id AND sh.active=1 AND m.user_id=?)) AND (l.occasion < ?)=? ORDER BY l.occasion,l.id LIMIT ? OFFSET ?`, uid, uid, s.clock().In(s.location).Format("2006-01-02"), archived, p.Limit, p.Offset)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, id := range ids {
		view, err := s.detail(ctx, v, id, Page{Limit: 1}, false)
		if err != nil {
			return nil, err
		}
		if view.Owner != nil {
			out = append(out, Summary{List: view.Owner.List, Count: view.Owner.Count})
		} else {
			out = append(out, Summary{List: view.Member.List, Count: view.Member.Count})
		}
	}
	return out, nil
}
