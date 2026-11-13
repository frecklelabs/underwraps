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
	groupMappings []GroupMapping
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
	s.groupMappings = append([]GroupMapping(nil), c.Groups...)
	s.adminGroups = append([]string(nil), c.AdministratorGroups...)
	s.staticAdmins = map[Identity]bool{}
	seenGroups := map[int64]bool{}
	for _, g := range c.Groups {
		if g.ID <= 0 || g.Claim == "" || g.Name == "" || seenGroups[g.ID] {
			db.Close()
			return nil, ErrInvalid
		}
		seenGroups[g.ID] = true
	}
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
	if version > 1 {
		return fmt.Errorf("database schema %d is newer than supported schema 1", version)
	}
	if version == 1 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(schema)
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
	var stored string
	err = s.db.QueryRowContext(ctx, "SELECT name FROM users WHERE id=?", id).Scan(&stored)
	return User{ID: id, Identity: v, Name: stored}, err
}
func (s *Store) Users(ctx context.Context, v Identity) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admin(ctx, v); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,issuer,subject,name,disabled FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err = rows.Scan(&u.ID, &u.Identity.Issuer, &u.Identity.Subject, &u.Name, &u.Disabled); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) SetUser(ctx context.Context, v Identity, id int64, name string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admin(ctx, v); err != nil {
		return err
	}
	if name == "" {
		return ErrInvalid
	}
	r, err := s.db.ExecContext(ctx, "UPDATE users SET name=?,disabled=? WHERE id=?", name, disabled, id)
	return changed(r, err)
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
func (s *Store) Groups(ctx context.Context, v Identity) ([]Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admin(ctx, v); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,disabled FROM groups ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err = rows.Scan(&g.ID, &g.Name, &g.Disabled); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
func (s *Store) CreateGroup(ctx context.Context, v Identity, name string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admin(ctx, v); err != nil {
		return 0, err
	}
	if name == "" {
		return 0, ErrInvalid
	}
	r, err := s.db.ExecContext(ctx, "INSERT INTO groups(name) VALUES(?)", name)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}
func (s *Store) SetGroup(ctx context.Context, v Identity, id int64, name string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admin(ctx, v); err != nil {
		return err
	}
	if name == "" {
		return ErrInvalid
	}
	r, err := s.db.ExecContext(ctx, "UPDATE groups SET name=?,disabled=? WHERE id=?", name, disabled, id)
	return changed(r, err)
}
func (s *Store) SetMembership(ctx context.Context, v Identity, group, user int64, active bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admin(ctx, v); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM groups g,users u WHERE g.id=? AND u.id=?", group, user).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrMissing
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO memberships VALUES(?,?,?) ON CONFLICT(group_id,user_id) DO UPDATE SET active=excluded.active", group, user, active)
	return err
}
