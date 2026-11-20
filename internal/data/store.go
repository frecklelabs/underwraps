package data

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
	_ "time/tzdata"
)

type Store struct {
	mu            sync.Mutex
	db            *sql.DB
	location      *time.Location
	clock         func() time.Time
	admins        map[Identity]bool
	staticAdmins  map[Identity]bool
	issuer        string
	requiredGroup string
	adminGroups   []string
}

// Open is the sole database-file opener. The directory must be private to the app.
func Open(c Config) (*Store, error) {
	if c.Path == "" || c.Timezone == "" || c.Clock == nil {
		return nil, ErrInvalid
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(c.Path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(c.Path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(c.Path)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, location: loc, clock: c.Clock, admins: map[Identity]bool{}}
	s.issuer = c.Issuer
	s.requiredGroup = c.RequiredGroup
	s.adminGroups = append([]string(nil), c.AdministratorGroups...)
	s.staticAdmins = map[Identity]bool{}
	for _, id := range c.Administrators {
		if !validIdentity(id) {
			db.Close()
			return nil, ErrInvalid
		}
		s.admins[id] = true
		s.staticAdmins[id] = true
	}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error       { return s.db.Close() }
func validIdentity(v Identity) bool { return v.Issuer != "" && v.Subject != "" }
func (s *Store) now() string        { return s.clock().UTC().Format(time.RFC3339Nano) }
func (s *Store) archived(date string) bool {
	return s.clock().In(s.location).Format("2006-01-02") > date
}
func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return fmt.Errorf("database schema %d is newer than supported schema 2", version)
	}
	if version == 2 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if version == 0 {
		_, err = tx.Exec(schema)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(schemaV2)
	if err != nil {
		return err
	}
	return tx.Commit()
}

const schema = `
CREATE TABLE users(id INTEGER PRIMARY KEY,issuer TEXT NOT NULL,subject TEXT NOT NULL,name TEXT NOT NULL,disabled INTEGER NOT NULL DEFAULT 0,UNIQUE(issuer,subject));
CREATE TABLE groups(id INTEGER PRIMARY KEY,name TEXT NOT NULL,disabled INTEGER NOT NULL DEFAULT 0);
CREATE TABLE memberships(group_id INTEGER REFERENCES groups(id),user_id INTEGER REFERENCES users(id),active INTEGER NOT NULL,PRIMARY KEY(group_id,user_id));
CREATE TABLE lists(id INTEGER PRIMARY KEY,owner_id INTEGER NOT NULL REFERENCES users(id),name TEXT NOT NULL,occasion TEXT NOT NULL,created TEXT NOT NULL,updated TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE shares(list_id INTEGER REFERENCES lists(id),group_id INTEGER REFERENCES groups(id),active INTEGER NOT NULL,PRIMARY KEY(list_id,group_id));
CREATE TABLE items(id INTEGER PRIMARY KEY,list_id INTEGER NOT NULL REFERENCES lists(id),creator_id INTEGER NOT NULL REFERENCES users(id),surprise INTEGER NOT NULL,name TEXT NOT NULL,link TEXT NOT NULL,price TEXT NOT NULL,note TEXT NOT NULL,image TEXT NOT NULL,quantity INTEGER NOT NULL CHECK(quantity>0),priority INTEGER NOT NULL,created TEXT NOT NULL,updated TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1,deleted INTEGER NOT NULL DEFAULT 0);
CREATE TABLE claims(item_id INTEGER PRIMARY KEY REFERENCES items(id),buyer_id INTEGER NOT NULL REFERENCES users(id),active INTEGER NOT NULL DEFAULT 1);
CREATE TABLE coordination(id INTEGER PRIMARY KEY,item_id INTEGER NOT NULL REFERENCES items(id),actor_id INTEGER NOT NULL REFERENCES users(id),action TEXT NOT NULL,at TEXT NOT NULL);
CREATE INDEX items_list ON items(list_id,priority,id);
PRAGMA user_version=1;
`

func (s *Store) user(ctx context.Context, v Identity) (int64, error) {
	if !validIdentity(v) {
		return 0, ErrMissing
	}
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM users WHERE issuer=? AND subject=? AND disabled=0", v.Issuer, v.Subject).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, ErrMissing
	}
	return id, err
}
func (s *Store) admin(ctx context.Context, v Identity) error {
	if _, err := s.user(ctx, v); err != nil {
		return err
	}
	if !s.admins[v] {
		return ErrMissing
	}
	return nil
}

// Register records only the verified caller. Disabled identities cannot reactivate themselves.
func (s *Store) Register(ctx context.Context, v Identity, name string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validIdentity(v) || name == "" {
		return User{}, ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO users(issuer,subject,name) VALUES(?,?,?) ON CONFLICT(issuer,subject) DO NOTHING", v.Issuer, v.Subject, name)
	if err != nil {
		return User{}, err
	}
	id, err := s.user(ctx, v)
	if err != nil {
		return User{}, err
	}
	var u User
	u.ID = id
	u.Identity = v
	err = s.db.QueryRowContext(ctx, "SELECT name,can_share_own FROM users WHERE id=?", id).Scan(&u.Name, &u.CanShareOwn)
	return u, err
}
func (s *Store) Users(ctx context.Context, v Identity) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admin(ctx, v); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,issuer,subject,name,disabled,can_share_own FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err = rows.Scan(&u.ID, &u.Identity.Issuer, &u.Identity.Subject, &u.Name, &u.Disabled, &u.CanShareOwn); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		out[i].DefaultManagers = []int64{}
		rs, e := s.db.QueryContext(ctx, "SELECT manager_id FROM default_managers WHERE user_id=? ORDER BY manager_id", out[i].ID)
		if e != nil {
			return nil, e
		}
		for rs.Next() {
			var id int64
			if e = rs.Scan(&id); e != nil {
				rs.Close()
				return nil, e
			}
			out[i].DefaultManagers = append(out[i].DefaultManagers, id)
		}
		e = rs.Err()
		rs.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Store) SetUser(ctx context.Context, v Identity, id int64, name string, disabled, canShareOwn bool, managers []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admin(ctx, v); err != nil {
		return err
	}
	if name == "" {
		return ErrInvalid
	}
	actor, err := s.user(ctx, v)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, m := range managers {
		if m == id || m == actor || seen[m] {
			return ErrInvalid
		}
		seen[m] = true
		var n int
		if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE id=? AND disabled=0", m).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return ErrInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if disabled {
		// Account settings must not bypass the last-manager sharing guard. Check
		// every affected list, and keep the rejection independent of its content.
		var orphaned bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM list_access a
			JOIN users target ON target.id=a.user_id
			JOIN lists l ON l.id=a.list_id
			JOIN users owner ON owner.id=l.owner_id
			WHERE a.user_id=? AND target.disabled=0 AND a.active=1 AND a.role='manager'
			AND owner.can_share_own=0
			AND NOT EXISTS (
				SELECT 1 FROM list_access other JOIN users u ON u.id=other.user_id
				WHERE other.list_id=a.list_id AND other.user_id!=a.user_id
				AND other.active=1 AND other.role='manager' AND u.disabled=0
			))`, id).Scan(&orphaned)
		if err != nil {
			return err
		}
		if orphaned {
			return ErrInvalid
		}
	}
	r, err := tx.ExecContext(ctx, "UPDATE users SET name=?,disabled=?,can_share_own=? WHERE id=?", name, disabled, canShareOwn, id)
	if err = changed(r, err); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM default_managers WHERE user_id=?", id); err != nil {
		return err
	}
	for _, m := range managers {
		if _, err = tx.ExecContext(ctx, "INSERT INTO default_managers VALUES(?,?)", id, m); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func changed(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ErrMissing
	}
	return err
}

// Legacy group grants are deliberately not expanded into per-person access: the
// old sibling visibility is unsafe. Owners retain lists; new grants are explicit.
const schemaV2 = `
ALTER TABLE users ADD COLUMN can_share_own INTEGER NOT NULL DEFAULT 0 CHECK(can_share_own IN (0,1));
CREATE TABLE list_access(list_id INTEGER NOT NULL REFERENCES lists(id),user_id INTEGER NOT NULL REFERENCES users(id),role TEXT NOT NULL CHECK(role IN ('viewer','manager')),active INTEGER NOT NULL CHECK(active IN (0,1)),PRIMARY KEY(list_id,user_id));
CREATE TABLE default_managers(user_id INTEGER NOT NULL REFERENCES users(id),manager_id INTEGER NOT NULL REFERENCES users(id),PRIMARY KEY(user_id,manager_id),CHECK(user_id != manager_id));
CREATE TRIGGER access_not_owner BEFORE INSERT ON list_access WHEN NEW.user_id=(SELECT owner_id FROM lists WHERE id=NEW.list_id) BEGIN SELECT RAISE(ABORT,'owner access'); END;
CREATE TRIGGER access_not_owner_update BEFORE UPDATE ON list_access WHEN NEW.user_id=(SELECT owner_id FROM lists WHERE id=NEW.list_id) BEGIN SELECT RAISE(ABORT,'owner access'); END;
DROP TABLE shares;
DROP TABLE memberships;
DROP TABLE groups;
PRAGMA user_version=2;
`
