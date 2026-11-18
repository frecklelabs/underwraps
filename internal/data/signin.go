package data

import "context"

// GroupMapping binds a verified claim to stable configured group metadata.
type GroupMapping struct {
	Claim string
	ID    int64
	Name  string
}
type Account struct {
	User   User
	Groups []Group
	Admin  bool
}

// SignIn accepts claims only after cryptographic verification by the transport.
// It reconciles only this caller's memberships, never grants content access to admins.
func (s *Store) SignIn(ctx context.Context, v Identity, name string, claims []string) (Account, error) {
	if v.Issuer != s.issuer || !validIdentity(v) {
		return Account{}, ErrInvalid
	}
	u, err := s.Register(ctx, v, name)
	if err != nil {
		return Account{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	present := map[string]bool{}
	for _, c := range claims {
		present[c] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	out := Account{User: u, Groups: []Group{}}
	for _, g := range s.groupMappings {
		// Disabled metadata stays disabled across sign-ins.
		_, err = tx.ExecContext(ctx, `INSERT INTO groups(id,name) VALUES(?,?) ON CONFLICT(id) DO NOTHING`, g.ID, g.Name)
		if err != nil {
			return Account{}, err
		}
		active := present[g.Claim]
		_, err = tx.ExecContext(ctx, `INSERT INTO memberships VALUES(?,?,?) ON CONFLICT(group_id,user_id) DO UPDATE SET active=excluded.active`, g.ID, u.ID, active)
		if err != nil {
			return Account{}, err
		}
		var group Group
		err = tx.QueryRowContext(ctx, `SELECT id,name,disabled FROM groups WHERE id=?`, g.ID).Scan(&group.ID, &group.Name, &group.Disabled)
		if err != nil {
			return Account{}, err
		}
		if active && !group.Disabled {
			out.Groups = append(out.Groups, group)
		}
	}
	if err = tx.Commit(); err != nil {
		return Account{}, err
	}
	admin := s.staticAdmins[v]
	for _, c := range s.adminGroups {
		admin = admin || present[c]
	}
	s.admins[v] = admin
	out.Admin = admin
	return out, nil
}
