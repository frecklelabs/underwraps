package data

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"strings"
)

// Random IDs prevent hidden insertions from being inferred from sequence gaps.
func newID() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b[:]) & 0x7fffffffffffffff), nil
}
func validRequest(r Request) bool {
	return strings.TrimSpace(r.Name) != "" && r.Quantity > 0 && r.Priority >= 0
}
func (s *Store) AddRequest(ctx context.Context, v Identity, list int64, r Request) (OwnerItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, u, err := s.writable(ctx, v, list, true)
	if err != nil {
		return OwnerItem{}, err
	}
	return s.insert(ctx, list, u, r, false)
}
func (s *Store) AddSurprise(ctx context.Context, v Identity, list int64, r Request) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, u, err := s.writable(ctx, v, list, false)
	if err != nil {
		return 0, err
	}
	if u == l.OwnerID {
		return 0, ErrMissing
	}
	i, err := s.insert(ctx, list, u, r, true)
	return i.ID, err
}
func (s *Store) insert(ctx context.Context, list, u int64, r Request, surprise bool) (OwnerItem, error) {
	if !validRequest(r) {
		return OwnerItem{}, ErrInvalid
	}
	id, err := newID()
	if err != nil {
		return OwnerItem{}, err
	}
	now := s.now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO items(id,list_id,creator_id,surprise,name,link,price,note,image,quantity,priority,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, list, u, surprise, r.Name, r.Link, r.Price, r.Note, r.Image, r.Quantity, r.Priority, now, now)
	if err != nil {
		return OwnerItem{}, err
	}
	return OwnerItem{ID: id, Request: r, CreatedAt: now, UpdatedAt: now, Revision: 1}, nil
}
func (s *Store) requested(ctx context.Context, v Identity, list, id int64) (OwnerItem, error) {
	if _, _, err := s.writable(ctx, v, list, true); err != nil {
		return OwnerItem{}, err
	}
	var i OwnerItem
	err := s.db.QueryRowContext(ctx, `SELECT id,name,link,price,note,image,quantity,priority,created,updated,revision,deleted FROM items WHERE id=? AND list_id=? AND surprise=0`, id, list).Scan(&i.ID, &i.Name, &i.Link, &i.Price, &i.Note, &i.Image, &i.Quantity, &i.Priority, &i.CreatedAt, &i.UpdatedAt, &i.Revision, &i.Deleted)
	if err == sql.ErrNoRows {
		return OwnerItem{}, ErrMissing
	}
	return i, err
}
func (s *Store) UpdateRequest(ctx context.Context, v Identity, list, id int64, r Request) (OwnerItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.requested(ctx, v, list, id)
	if err != nil {
		return OwnerItem{}, err
	}
	if i.Deleted {
		return OwnerItem{}, ErrMissing
	}
	if !validRequest(r) {
		return OwnerItem{}, ErrInvalid
	}
	now := s.now()
	_, err = s.db.ExecContext(ctx, `UPDATE items SET name=?,link=?,price=?,note=?,image=?,quantity=?,priority=?,updated=?,revision=revision+1 WHERE id=?`, r.Name, r.Link, r.Price, r.Note, r.Image, r.Quantity, r.Priority, now, id)
	if err != nil {
		return OwnerItem{}, err
	}
	i.Request = r
	i.UpdatedAt = now
	i.Revision++
	return i, nil
}
func (s *Store) DeleteRequest(ctx context.Context, v Identity, list, id int64) (OwnerItem, error) {
	return s.setDeleted(ctx, v, list, id, true)
}
func (s *Store) RestoreRequest(ctx context.Context, v Identity, list, id int64) (OwnerItem, error) {
	return s.setDeleted(ctx, v, list, id, false)
}
func (s *Store) setDeleted(ctx context.Context, v Identity, list, id int64, deleted bool) (OwnerItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.requested(ctx, v, list, id)
	if err != nil {
		return OwnerItem{}, err
	}
	if i.Deleted == deleted {
		return i, nil
	}
	now := s.now()
	_, err = s.db.ExecContext(ctx, "UPDATE items SET deleted=?,updated=?,revision=revision+1 WHERE id=?", deleted, now, id)
	if err != nil {
		return OwnerItem{}, err
	}
	i.Deleted = deleted
	i.UpdatedAt = now
	i.Revision++
	return i, nil
}

// Claims are bought markers. A buyer may release their own marker; history is retained.
func (s *Store) Claim(ctx context.Context, v Identity, list, item int64) error {
	return s.claim(ctx, v, list, item, true)
}
func (s *Store) ReleaseClaim(ctx context.Context, v Identity, list, item int64) error {
	return s.claim(ctx, v, list, item, false)
}
func (s *Store) claim(ctx context.Context, v Identity, list, item int64, buy bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, u, err := s.writable(ctx, v, list, false)
	if err != nil {
		return err
	}
	if u == l.OwnerID {
		return ErrMissing
	}
	var deleted bool
	var buyer sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT i.deleted,c.buyer_id FROM items i LEFT JOIN claims c ON c.item_id=i.id AND c.active=1 WHERE i.id=? AND i.list_id=? AND (i.deleted=0 OR (i.surprise=0 AND c.buyer_id IS NOT NULL))`, item, list).Scan(&deleted, &buyer)
	if err == sql.ErrNoRows {
		return ErrMissing
	}
	if err != nil {
		return err
	}
	if buy && deleted {
		return ErrMissing
	}
	if buy && buyer.Valid {
		return ErrConflict
	}
	if !buy && (!buyer.Valid || buyer.Int64 != u) {
		return ErrMissing
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	action := "claim"
	if buy {
		_, err = tx.ExecContext(ctx, "INSERT INTO claims(item_id,buyer_id,active) VALUES(?,?,1) ON CONFLICT(item_id) DO UPDATE SET buyer_id=excluded.buyer_id,active=1 WHERE claims.active=0", item, u)
		if err == nil {
			var n int
			err = tx.QueryRowContext(ctx, "SELECT changes()").Scan(&n)
			if err == nil && n == 0 {
				return ErrConflict
			}
		}
	} else {
		action = "release"
		_, err = tx.ExecContext(ctx, "UPDATE claims SET active=0 WHERE item_id=? AND buyer_id=?", item, u)
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO coordination(item_id,actor_id,action,at) VALUES(?,?,?,?)", item, u, action, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// A surprise creator may soft-delete their entry; coordination history survives.
func (s *Store) DeleteSurprise(ctx context.Context, v Identity, list, item int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, u, err := s.writable(ctx, v, list, false)
	if err != nil {
		return err
	}
	if l.OwnerID == u {
		return ErrMissing
	}
	r, err := s.db.ExecContext(ctx, "UPDATE items SET deleted=1 WHERE id=? AND list_id=? AND surprise=1 AND creator_id=?", item, list, u)
	return changed(r, err)
}
