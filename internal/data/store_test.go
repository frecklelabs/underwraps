package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()
var page = Page{Limit: 100}

type fixture struct {
	s                                     *Store
	path                                  string
	now                                   *time.Time
	admin, owner, member, other, outsider Identity
	users                                 []User
	group, foreign                        int64
	list                                  List
}

func setup(t *testing.T) *fixture {
	t.Helper()
	now := time.Date(2030, 12, 25, 12, 0, 0, 0, time.UTC)
	f := &fixture{path: filepath.Join(t.TempDir(), "test.sqlite"), now: &now, admin: Identity{"https://login.example.invalid", "admin"}, owner: Identity{"https://login.example.invalid", "owner"}, member: Identity{"https://login.example.invalid", "member"}, other: Identity{"https://login.example.invalid", "other"}, outsider: Identity{"https://login.example.invalid", "outsider"}}
	var err error
	f.s, err = Open(Config{Path: f.path, Timezone: "America/Chicago", Clock: func() time.Time { return *f.now }, Administrators: []Identity{f.admin}})
	must(t, err)
	t.Cleanup(func() { f.s.Close() })
	for _, v := range []Identity{f.admin, f.owner, f.member, f.other, f.outsider} {
		u, err := f.s.Register(ctx, v, v.Subject)
		must(t, err)
		f.users = append(f.users, u)
	}
	f.group, err = f.s.CreateGroup(ctx, f.admin, "household-example")
	must(t, err)
	f.foreign, err = f.s.CreateGroup(ctx, f.admin, "other-example")
	must(t, err)
	for _, u := range f.users[1:4] {
		must(t, f.s.SetMembership(ctx, f.admin, f.group, u.ID, true))
	}
	must(t, f.s.SetMembership(ctx, f.admin, f.foreign, f.users[4].ID, true))
	f.list, err = f.s.CreateList(ctx, f.owner, "winter", "2030-12-25", []int64{f.group})
	must(t, err)
	return f
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
}
func request(name string) Request {
	return Request{Name: name, Link: "https://shop.example.invalid/item", Price: "12.00", Note: "example", Image: "https://shop.example.invalid/image", Quantity: 1, Priority: 3}
}
func add(t *testing.T, f *fixture, name string) OwnerItem {
	t.Helper()
	i, err := f.s.AddRequest(ctx, f.owner, f.list.ID, request(name))
	must(t, err)
	return i
}
func snapshot(t *testing.T, f *fixture) string {
	t.Helper()
	out := []any{}
	for _, p := range []Page{page, {Limit: 1}, {Offset: 1, Limit: 1}, {Offset: 99, Limit: 1}} {
		v, err := f.s.ListDetail(ctx, f.owner, f.list.ID, p)
		must(t, err)
		out = append(out, v)
		d, err := f.s.DeletedItems(ctx, f.owner, f.list.ID, p)
		must(t, err)
		out = append(out, d)
		for _, arch := range []bool{false, true} {
			v, err := f.s.Lists(ctx, f.owner, arch, p)
			must(t, err)
			out = append(out, v)
		}
	}
	b, err := json.Marshal(out)
	must(t, err)
	return string(b)
}
func TestReadMatrix(t *testing.T) {
	for _, arch := range []bool{false, true} {
		for _, state := range []string{"requested", "claimed", "surprise", "deleted", "deleted-bought"} {
			t.Run(fmt.Sprint(arch, "/", state), func(t *testing.T) {
				f := setup(t)
				i := add(t, f, "request")
				if state == "claimed" || state == "deleted-bought" {
					must(t, f.s.Claim(ctx, f.member, f.list.ID, i.ID))
				}
				if state == "surprise" {
					_, err := f.s.AddSurprise(ctx, f.member, f.list.ID, request("hidden"))
					must(t, err)
				}
				if state == "deleted" || state == "deleted-bought" {
					_, err := f.s.DeleteRequest(ctx, f.owner, f.list.ID, i.ID)
					must(t, err)
				}
				if arch {
					*f.now = f.now.Add(24 * time.Hour)
				}
				for _, v := range []Identity{f.owner, f.member, f.other, f.outsider, f.admin} {
					t.Run(v.Subject, func(t *testing.T) {
						allowed := v == f.owner || v == f.member || v == f.other
						detail, err := f.s.ListDetail(ctx, v, f.list.ID, page)
						if !allowed {
							wantErr(t, err, ErrMissing)
						} else {
							must(t, err)
							if v == f.owner {
								expected := 1
								if state == "deleted" || state == "deleted-bought" {
									expected = 0
								}
								if detail.Member != nil || detail.Owner.Count != expected || len(detail.Owner.Items) != expected || detail.Owner.List.Archived != arch {
									t.Fatalf("owner %+v", detail)
								}
							} else {
								expected := 1
								if state == "deleted" {
									expected = 0
								}
								if state == "surprise" {
									expected = 2
								}
								if detail.Owner != nil || detail.Member.Count != expected || len(detail.Member.Items) != expected || detail.Member.List.Archived != arch {
									t.Fatalf("member %+v", detail)
								}
								for _, item := range detail.Member.Items {
									if item.ID == i.ID {
										if (item.BuyerID != nil) != (state == "claimed" || state == "deleted-bought") {
											t.Fatal("claim projection")
										}
										if item.RemovedByOwner != (state == "deleted-bought") {
											t.Fatal("removed marker")
										}
									}
								}
							}
						}
						deleted, err := f.s.DeletedItems(ctx, v, f.list.ID, page)
						if v != f.owner {
							wantErr(t, err, ErrMissing)
						} else {
							must(t, err)
							n := 0
							if state == "deleted" || state == "deleted-bought" {
								n = 1
							}
							if deleted.Count != n || len(deleted.Items) != n {
								t.Fatal("deleted projection")
							}
						}
						for _, a := range []bool{false, true} {
							lists, err := f.s.Lists(ctx, v, a, page)
							must(t, err)
							n := 0
							if allowed && a == arch {
								n = 1
							}
							if len(lists) != n {
								t.Fatalf("discovery %v", lists)
							}
							if n == 1 {
								count := 0
								if detail.Owner != nil {
									count = detail.Owner.Count
								} else {
									count = detail.Member.Count
								}
								if lists[0].Count != count {
									t.Fatal("summary mismatch")
								}
							}
						}
					})
				}
			})
		}
	}
}
func TestOwnerDifferential(t *testing.T) {
	f := setup(t)
	a := add(t, f, "a")
	b := add(t, f, "b")
	before := snapshot(t, f)
	must(t, f.s.Claim(ctx, f.member, f.list.ID, a.ID))
	_, err := f.s.AddSurprise(ctx, f.member, f.list.ID, request("secret"))
	must(t, err)
	if snapshot(t, f) != before {
		t.Fatal("hidden activity changed owner responses")
	}
	// Reset only coordination through the public API, then replay identical owner
	// operations with and without a purchase against the same original request.
	must(t, f.s.ReleaseClaim(ctx, f.member, f.list.ID, a.ID))
	unbought, err := f.s.DeleteRequest(ctx, f.owner, f.list.ID, a.ID)
	must(t, err)
	_ = snapshot(t, f)
	restored, err := f.s.RestoreRequest(ctx, f.owner, f.list.ID, a.ID)
	must(t, err)
	must(t, f.s.Claim(ctx, f.member, f.list.ID, b.ID))
	bought, err := f.s.DeleteRequest(ctx, f.owner, f.list.ID, b.ID)
	must(t, err)
	restoredBought, err := f.s.RestoreRequest(ctx, f.owner, f.list.ID, b.ID)
	must(t, err)
	// IDs/names/creation are original-request fields, not coordination. Both edits
	// advance only their own request revisions with the same clock.
	unbought.ID = bought.ID
	unbought.Name = bought.Name
	restored.ID = restoredBought.ID
	restored.Name = restoredBought.Name
	if !reflect.DeepEqual(unbought, bought) || !reflect.DeepEqual(restored, restoredBought) {
		t.Fatal("mutation response discloses bought state")
	}
	_, err = f.s.DeleteRequest(ctx, f.owner, f.list.ID, a.ID)
	must(t, err)
	before = snapshot(t, f)
	wantErr(t, f.s.Claim(ctx, f.member, f.list.ID, b.ID), ErrConflict)
	if snapshot(t, f) != before {
		t.Fatal("claim error changed owner view")
	}
}
func TestPermissionsAndArchive(t *testing.T) {
	f := setup(t)
	i := add(t, f, "a")
	hidden, err := f.s.AddSurprise(ctx, f.member, f.list.ID, request("hidden"))
	must(t, err)
	for _, v := range []Identity{f.outsider, f.admin, Identity{"https://other.example.invalid", "owner"}, Identity{}} {
		_, err := f.s.ListDetail(ctx, v, f.list.ID, page)
		wantErr(t, err, ErrMissing)
		_, err = f.s.AddRequest(ctx, v, f.list.ID, request("x"))
		wantErr(t, err, ErrMissing)
		_, err = f.s.AddSurprise(ctx, v, f.list.ID, request("x"))
		wantErr(t, err, ErrMissing)
		_, err = f.s.UpdateRequest(ctx, v, f.list.ID, i.ID, request("x"))
		wantErr(t, err, ErrMissing)
		_, err = f.s.DeleteRequest(ctx, v, f.list.ID, i.ID)
		wantErr(t, err, ErrMissing)
		_, err = f.s.RestoreRequest(ctx, v, f.list.ID, i.ID)
		wantErr(t, err, ErrMissing)
		_, err = f.s.UpdateList(ctx, v, f.list.ID, "x", "2030-12-25", []int64{f.group})
		wantErr(t, err, ErrMissing)
		wantErr(t, f.s.Claim(ctx, v, f.list.ID, i.ID), ErrMissing)
		wantErr(t, f.s.ReleaseClaim(ctx, v, f.list.ID, i.ID), ErrMissing)
		wantErr(t, f.s.DeleteSurprise(ctx, v, f.list.ID, hidden), ErrMissing)
	}
	wantErr(t, f.s.Claim(ctx, f.owner, f.list.ID, i.ID), ErrMissing)
	_, err = f.s.AddSurprise(ctx, f.owner, f.list.ID, request("x"))
	wantErr(t, err, ErrMissing)
	_, err = f.s.CreateList(ctx, f.owner, "x", "2030-12-25", []int64{f.foreign})
	wantErr(t, err, ErrMissing)
	_, err = f.s.UpdateList(ctx, f.owner, f.list.ID, "x", "2030-12-25", []int64{f.foreign})
	wantErr(t, err, ErrMissing)
	_, err = f.s.UpdateRequest(ctx, f.owner, f.list.ID, hidden, request("x"))
	wantErr(t, err, ErrMissing)
	wantErr(t, f.s.DeleteSurprise(ctx, f.other, f.list.ID, hidden), ErrMissing)
	must(t, f.s.Claim(ctx, f.member, f.list.ID, i.ID))
	wantErr(t, f.s.ReleaseClaim(ctx, f.other, f.list.ID, i.ID), ErrMissing)
	// The occasion stays writable until midnight in the configured timezone.
	*f.now = time.Date(2030, 12, 26, 5, 59, 59, 0, time.UTC)
	_, err = f.s.UpdateRequest(ctx, f.owner, f.list.ID, i.ID, request("still active"))
	must(t, err)
	*f.now = f.now.Add(time.Second)
	for name, fn := range map[string]func() error{
		"add":      func() error { _, e := f.s.AddRequest(ctx, f.owner, f.list.ID, request("x")); return e },
		"surprise": func() error { _, e := f.s.AddSurprise(ctx, f.member, f.list.ID, request("x")); return e },
		"update":   func() error { _, e := f.s.UpdateRequest(ctx, f.owner, f.list.ID, i.ID, request("x")); return e },
		"delete":   func() error { _, e := f.s.DeleteRequest(ctx, f.owner, f.list.ID, i.ID); return e },
		"restore":  func() error { _, e := f.s.RestoreRequest(ctx, f.owner, f.list.ID, i.ID); return e },
		"list": func() error {
			_, e := f.s.UpdateList(ctx, f.owner, f.list.ID, "x", "2031-12-25", []int64{f.group})
			return e
		},
		"claim":           func() error { return f.s.Claim(ctx, f.other, f.list.ID, i.ID) },
		"release":         func() error { return f.s.ReleaseClaim(ctx, f.member, f.list.ID, i.ID) },
		"delete-surprise": func() error { return f.s.DeleteSurprise(ctx, f.member, f.list.ID, hidden) },
	} {
		t.Run(name, func(t *testing.T) { wantErr(t, fn(), ErrArchived) })
	}
	wantErr(t, f.s.Claim(ctx, f.outsider, f.list.ID, i.ID), ErrMissing)
}
func TestDeleteRestoreAndHistory(t *testing.T) {
	f := setup(t)
	i := add(t, f, "original")
	must(t, f.s.Claim(ctx, f.member, f.list.ID, i.ID))
	before := snapshot(t, f)
	_, err := f.s.DeleteRequest(ctx, f.owner, f.list.ID, i.ID)
	must(t, err)
	v, err := f.s.ListDetail(ctx, f.member, f.list.ID, page)
	must(t, err)
	if !v.Member.Items[0].RemovedByOwner || v.Member.Items[0].BuyerID == nil {
		t.Fatal("bought request lost")
	}
	_, err = f.s.RestoreRequest(ctx, f.owner, f.list.ID, i.ID)
	must(t, err)
	v, err = f.s.ListDetail(ctx, f.member, f.list.ID, page)
	must(t, err)
	if v.Member.Items[0].RemovedByOwner || v.Member.Items[0].BuyerID == nil {
		t.Fatal("claim lost on restore")
	}
	must(t, f.s.ReleaseClaim(ctx, f.member, f.list.ID, i.ID))
	var history int
	must(t, f.s.db.QueryRow("SELECT count(*) FROM coordination WHERE item_id=?", i.ID).Scan(&history))
	if history != 2 {
		t.Fatal("history lost")
	}
	_ = before
	_, err = f.s.DeleteRequest(ctx, f.owner, f.list.ID, i.ID)
	must(t, err)
	v, err = f.s.ListDetail(ctx, f.member, f.list.ID, page)
	must(t, err)
	if v.Member.Count != 0 {
		t.Fatal("unbought removed request visible")
	}
	wantErr(t, f.s.Claim(ctx, f.member, f.list.ID, i.ID), ErrMissing)
}
func TestClaimRace(t *testing.T) {
	f := setup(t)
	i := add(t, f, "race")
	second, err := Open(Config{Path: f.path, Timezone: "America/Chicago", Clock: func() time.Time { return *f.now }})
	must(t, err)
	defer second.Close()
	start := make(chan struct{})
	out := make(chan error, 2)
	var wg sync.WaitGroup
	for n, v := range []Identity{f.member, f.other} {
		wg.Add(1)
		go func(n int, v Identity) {
			defer wg.Done()
			<-start
			s := f.s
			if n == 1 {
				s = second
			}
			out <- s.Claim(ctx, v, f.list.ID, i.ID)
		}(n, v)
	}
	close(start)
	wg.Wait()
	close(out)
	wins := 0
	conflicts := 0
	for err := range out {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}
func TestAdminAndMembership(t *testing.T) {
	f := setup(t)
	for _, v := range []Identity{f.owner, f.member, f.outsider} {
		_, err := f.s.Users(ctx, v)
		wantErr(t, err, ErrMissing)
		_, err = f.s.Groups(ctx, v)
		wantErr(t, err, ErrMissing)
		_, err = f.s.CreateGroup(ctx, v, "x")
		wantErr(t, err, ErrMissing)
		wantErr(t, f.s.SetMembership(ctx, v, f.group, f.users[4].ID, true), ErrMissing)
		wantErr(t, f.s.SetGroup(ctx, v, f.group, "x", true), ErrMissing)
		wantErr(t, f.s.SetUser(ctx, v, f.users[1].ID, "x", true), ErrMissing)
	}
	users, err := f.s.Users(ctx, f.admin)
	must(t, err)
	if len(users) != 5 {
		t.Fatal(users)
	}
	groups, err := f.s.Groups(ctx, f.admin)
	must(t, err)
	if len(groups) != 2 {
		t.Fatal(groups)
	}
	// Changing names does not change the issuer/subject binding.
	must(t, f.s.SetUser(ctx, f.admin, f.users[2].ID, "renamed", false))
	u, err := f.s.Register(ctx, f.member, "different")
	must(t, err)
	if u.ID != f.users[2].ID || u.Name != "renamed" {
		t.Fatal(u)
	}
	must(t, f.s.SetMembership(ctx, f.admin, f.group, u.ID, false))
	_, err = f.s.ListDetail(ctx, f.member, f.list.ID, page)
	wantErr(t, err, ErrMissing)
	must(t, f.s.SetMembership(ctx, f.admin, f.group, u.ID, true))
	must(t, f.s.SetGroup(ctx, f.admin, f.group, "example", true))
	_, err = f.s.ListDetail(ctx, f.member, f.list.ID, page)
	wantErr(t, err, ErrMissing)
	_, err = f.s.ListDetail(ctx, f.owner, f.list.ID, page)
	must(t, err)
	must(t, f.s.SetGroup(ctx, f.admin, f.group, "example", false))
	must(t, f.s.SetUser(ctx, f.admin, u.ID, "renamed", true))
	_, err = f.s.Register(ctx, f.member, "renamed")
	wantErr(t, err, ErrMissing)
	_, err = f.s.ListDetail(ctx, f.member, f.list.ID, page)
	wantErr(t, err, ErrMissing)
}
func TestCrossListAndPagination(t *testing.T) {
	f := setup(t)
	i := add(t, f, "one")
	two := request("two")
	two.Priority = 9
	j, err := f.s.AddRequest(ctx, f.owner, f.list.ID, two)
	must(t, err)
	foreign, err := f.s.CreateList(ctx, f.outsider, "other", "2030-12-25", []int64{f.foreign})
	must(t, err)
	k, err := f.s.AddRequest(ctx, f.outsider, foreign.ID, request("foreign"))
	must(t, err)
	for _, id := range []int64{k.ID, 999} {
		_, err = f.s.UpdateRequest(ctx, f.owner, f.list.ID, id, request("x"))
		wantErr(t, err, ErrMissing)
		_, err = f.s.DeleteRequest(ctx, f.owner, f.list.ID, id)
		wantErr(t, err, ErrMissing)
		_, err = f.s.RestoreRequest(ctx, f.owner, f.list.ID, id)
		wantErr(t, err, ErrMissing)
		wantErr(t, f.s.Claim(ctx, f.member, f.list.ID, id), ErrMissing)
	}
	for _, v := range []Identity{f.owner, f.member} {
		for n, id := range []int64{j.ID, i.ID} {
			view, err := f.s.ListDetail(ctx, v, f.list.ID, Page{Offset: n, Limit: 1})
			must(t, err)
			if view.Owner != nil {
				if view.Owner.Count != 2 || len(view.Owner.Items) != 1 || view.Owner.Items[0].ID != id {
					t.Fatal(view)
				}
			} else {
				if view.Member.Count != 2 || len(view.Member.Items) != 1 || view.Member.Items[0].ID != id {
					t.Fatal(view)
				}
			}
		}
	}
	for _, p := range []Page{{}, {Limit: 101}, {Offset: -1, Limit: 1}} {
		_, err = f.s.ListDetail(ctx, f.owner, f.list.ID, p)
		wantErr(t, err, ErrInvalid)
		_, err = f.s.DeletedItems(ctx, f.owner, f.list.ID, p)
		wantErr(t, err, ErrInvalid)
		_, err = f.s.Lists(ctx, f.owner, false, p)
		wantErr(t, err, ErrInvalid)
	}
}
func TestPersistenceAndMigration(t *testing.T) {
	f := setup(t)
	i := add(t, f, "persistent")
	must(t, f.s.Close())
	s, err := Open(Config{Path: f.path, Timezone: "America/Chicago", Clock: func() time.Time { return *f.now }})
	must(t, err)
	defer s.Close()
	view, err := s.ListDetail(ctx, f.owner, f.list.ID, page)
	must(t, err)
	if view.Owner.Items[0].ID != i.ID {
		t.Fatal("lost record")
	}
	_, err = s.db.Exec("PRAGMA user_version=2")
	must(t, err)
	_, err = Open(Config{Path: f.path, Timezone: "America/Chicago", Clock: time.Now})
	if err == nil {
		t.Fatal("opened newer schema")
	}
}

// Identical on-disk starting databases give both worlds the same original IDs,
// metadata and request history. Compare complete serialized responses, not a
// curated set of fields that might miss a new side channel.
func TestOwnerDifferentialWorlds(t *testing.T) {
	f := setup(t)
	i := add(t, f, "original")
	j := add(t, f, "second")
	_, err := f.s.CreateList(ctx, f.owner, "later", "2030-12-26", []int64{f.group})
	must(t, err)
	must(t, f.s.Close())
	raw, err := os.ReadFile(f.path)
	must(t, err)
	worlds := []*fixture{}
	for n := 0; n < 2; n++ {
		copy := *f
		copy.path = filepath.Join(t.TempDir(), "world.sqlite")
		must(t, os.WriteFile(copy.path, raw, 0600))
		copy.s, err = Open(Config{Path: copy.path, Timezone: "America/Chicago", Clock: func() time.Time { return *f.now }})
		must(t, err)
		defer copy.s.Close()
		worlds = append(worlds, &copy)
	}
	must(t, worlds[1].s.Claim(ctx, f.member, f.list.ID, i.ID))
	must(t, worlds[1].s.Claim(ctx, f.member, f.list.ID, j.ID))
	hidden, err := worlds[1].s.AddSurprise(ctx, f.member, f.list.ID, request("hidden"))
	must(t, err)
	compare := func() {
		t.Helper()
		if snapshot(t, worlds[0]) != snapshot(t, worlds[1]) {
			t.Fatal("owner worlds differ")
		}
	}
	compare()
	for _, fn := range []func(*fixture) (OwnerItem, error){
		func(w *fixture) (OwnerItem, error) {
			return w.s.UpdateRequest(ctx, w.owner, w.list.ID, i.ID, request("edited"))
		},
		func(w *fixture) (OwnerItem, error) { return w.s.DeleteRequest(ctx, w.owner, w.list.ID, i.ID) },
		func(w *fixture) (OwnerItem, error) { return w.s.DeleteRequest(ctx, w.owner, w.list.ID, i.ID) },
		func(w *fixture) (OwnerItem, error) { return w.s.RestoreRequest(ctx, w.owner, w.list.ID, i.ID) },
		func(w *fixture) (OwnerItem, error) { return w.s.RestoreRequest(ctx, w.owner, w.list.ID, i.ID) },
		func(w *fixture) (OwnerItem, error) { return w.s.DeleteRequest(ctx, w.owner, w.list.ID, j.ID) },
		func(w *fixture) (OwnerItem, error) {
			return w.s.UpdateRequest(ctx, w.owner, w.list.ID, hidden, request("guessed"))
		},
		func(w *fixture) (OwnerItem, error) { return w.s.DeleteRequest(ctx, w.owner, w.list.ID, hidden) },
		func(w *fixture) (OwnerItem, error) { return w.s.RestoreRequest(ctx, w.owner, w.list.ID, hidden) },
	} {
		a, ae := fn(worlds[0])
		b, be := fn(worlds[1])
		if !reflect.DeepEqual(a, b) || !errors.Is(ae, be) {
			t.Fatalf("mutation worlds differ: %v / %v", ae, be)
		}
		compare()
	}
	*f.now = f.now.Add(48 * time.Hour)
	compare()
	for _, w := range worlds {
		_, err = w.s.RestoreRequest(ctx, w.owner, w.list.ID, j.ID)
		wantErr(t, err, ErrArchived)
		_, err = w.s.UpdateRequest(ctx, w.owner, w.list.ID, hidden, request("guessed"))
		wantErr(t, err, ErrArchived)
	}
	compare()
}
func TestMemberCannotEditRequestsOrShares(t *testing.T) {
	f := setup(t)
	i := add(t, f, "owned")
	for _, v := range []Identity{f.member, f.other} {
		_, err := f.s.AddRequest(ctx, v, f.list.ID, request("x"))
		wantErr(t, err, ErrMissing)
		_, err = f.s.UpdateRequest(ctx, v, f.list.ID, i.ID, request("x"))
		wantErr(t, err, ErrMissing)
		_, err = f.s.DeleteRequest(ctx, v, f.list.ID, i.ID)
		wantErr(t, err, ErrMissing)
		_, err = f.s.RestoreRequest(ctx, v, f.list.ID, i.ID)
		wantErr(t, err, ErrMissing)
		_, err = f.s.UpdateList(ctx, v, f.list.ID, "x", "2030-12-25", []int64{f.group})
		wantErr(t, err, ErrMissing)
	}
	surprise, err := f.s.AddSurprise(ctx, f.member, f.list.ID, request("gift"))
	must(t, err)
	must(t, f.s.Claim(ctx, f.other, f.list.ID, surprise))
	must(t, f.s.DeleteSurprise(ctx, f.member, f.list.ID, surprise))
	v, err := f.s.ListDetail(ctx, f.other, f.list.ID, page)
	must(t, err)
	if v.Member.Count != 1 {
		t.Fatal("soft-deleted surprise visible")
	}
	wantErr(t, f.s.Claim(ctx, f.other, f.list.ID, surprise), ErrMissing)
}
