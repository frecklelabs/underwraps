package data

import "context"

func (s *Store) People(ctx context.Context, v Identity) ([]Person, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.user(ctx, v); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,name FROM users ORDER BY name,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		if err = rows.Scan(&p.ID, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) sharingEditor(ctx context.Context, l List, uid int64) (bool, error) {
	var allowed bool
	if uid == l.OwnerID {
		err := s.db.QueryRowContext(ctx, "SELECT can_share_own FROM users WHERE id=?", uid).Scan(&allowed)
		return allowed, err
	}
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM list_access WHERE list_id=? AND user_id=? AND active=1 AND role='manager')", l.ID, uid).Scan(&allowed)
	return allowed, err
}
func (s *Store) Sharing(ctx context.Context, v Identity, list int64) (Sharing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, uid, err := s.access(ctx, v, list)
	if err != nil {
		return Sharing{}, err
	}
	allowed, err := s.sharingEditor(ctx, l, uid)
	if err != nil {
		return Sharing{}, err
	}
	out := Sharing{Access: []Access{}, CanEdit: allowed && !l.Archived}
	rows, err := s.db.QueryContext(ctx, "SELECT user_id,role FROM list_access WHERE list_id=? AND active=1 ORDER BY user_id", list)
	if err != nil {
		return Sharing{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Access
		if err = rows.Scan(&a.UserID, &a.Role); err != nil {
			return Sharing{}, err
		}
		out.Access = append(out.Access, a)
	}
	return out, rows.Err()
}

// SetSharing never changes owner-visible list timestamps or revisions.
func (s *Store) SetSharing(ctx context.Context, v Identity, list int64, access []Access) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, uid, err := s.access(ctx, v, list)
	if err != nil {
		return err
	}
	allowed, err := s.sharingEditor(ctx, l, uid)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrMissing
	}
	if l.Archived {
		return ErrArchived
	}
	seen := map[int64]bool{}
	managers := 0
	for _, a := range access {
		if a.UserID == l.OwnerID || seen[a.UserID] || (a.Role != "viewer" && a.Role != "manager") {
			return ErrInvalid
		}
		seen[a.UserID] = true
		var active bool
		if err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND disabled=0)", a.UserID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrInvalid
		}
		if a.Role == "manager" {
			managers++
		}
	}
	var ownerShares bool
	if err = s.db.QueryRowContext(ctx, "SELECT can_share_own FROM users WHERE id=?", l.OwnerID).Scan(&ownerShares); err != nil {
		return err
	}
	if !ownerShares && managers == 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE list_access SET active=0 WHERE list_id=?", list); err != nil {
		return err
	}
	for _, a := range access {
		if _, err = tx.ExecContext(ctx, "INSERT INTO list_access VALUES(?,?,?,1) ON CONFLICT(list_id,user_id) DO UPDATE SET role=excluded.role,active=1", list, a.UserID, a.Role); err != nil {
			return err
		}
	}
	return tx.Commit()
}
