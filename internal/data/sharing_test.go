package data

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSiblingSharing(t *testing.T) {
	f := setup(t)
	// Two children, two parents, and a separate metadata administrator.
	childA, childB := f.owner, f.outsider
	parentA, parentB := f.member, f.other
	defaults := []int64{f.users[2].ID, f.users[3].ID}
	for _, u := range []User{f.users[1], f.users[4]} {
		must(t, f.s.SetUser(ctx, f.admin, u.ID, u.Name, false, false, defaults))
	}
	lists := []List{}
	for _, child := range []Identity{childA, childB} {
		l, e := f.s.CreateList(ctx, child, "Celebration", "2030-12-25")
		must(t, e)
		lists = append(lists, l)
	}
	for n, child := range []Identity{childA, childB} {
		l := lists[n]
		sibling := []Identity{childB, childA}[n]
		i, e := f.s.AddRequest(ctx, child, l.ID, request("Wish"))
		must(t, e)
		before, e := f.s.ListDetail(ctx, child, l.ID, page)
		must(t, e)
		beforeJSON, _ := json.Marshal(before)
		must(t, f.s.Claim(ctx, parentA, l.ID, i.ID))
		_, e = f.s.AddSurprise(ctx, parentB, l.ID, request("Hidden gift"))
		must(t, e)
		for _, p := range []Identity{parentA, parentB} {
			v, e := f.s.ListDetail(ctx, p, l.ID, page)
			must(t, e)
			if len(v.Member.Items) != 2 {
				t.Fatal(v)
			}
		}
		for _, p := range []Identity{sibling, f.admin} {
			_, e = f.s.ListDetail(ctx, p, l.ID, page)
			wantErr(t, e, ErrMissing)
			_, e = f.s.Sharing(ctx, p, l.ID)
			wantErr(t, e, ErrMissing)
		}
		// A removed viewer's purchases persist for remaining viewers without leaking
		// through any owner content field, timestamp, count or revision.
		must(t, f.s.SetSharing(ctx, parentB, l.ID, []Access{{UserID: f.users[3].ID, Role: "manager"}}))
		_, e = f.s.ListDetail(ctx, parentA, l.ID, page)
		wantErr(t, e, ErrMissing)
		wantErr(t, f.s.ReleaseClaim(ctx, parentA, l.ID, i.ID), ErrMissing)
		after, e := f.s.ListDetail(ctx, child, l.ID, page)
		must(t, e)
		afterJSON, _ := json.Marshal(after)
		if string(beforeJSON) != string(afterJSON) {
			t.Fatal("sharing or purchase leaked to owner")
		}
		v, e := f.s.ListDetail(ctx, parentB, l.ID, page)
		must(t, e)
		found := false
		for _, item := range v.Member.Items {
			found = found || item.BuyerID != nil
		}
		if !found {
			t.Fatal("removed viewer purchase lost")
		}
	}
}

func TestSharingGuardRails(t *testing.T) {
	f := setup(t)
	manager := Access{UserID: f.users[3].ID, Role: "manager"}
	// New accounts fail closed; only defaults from admin settings seed grants.
	if f.users[1].CanShareOwn {
		t.Fatal("can-share-own default")
	}
	for _, v := range []Identity{f.owner, f.member, f.admin, f.outsider} {
		wantErr(t, f.s.SetSharing(ctx, v, f.list.ID, []Access{manager}), ErrMissing)
	}
	for _, grants := range [][]Access{nil, {{UserID: f.users[1].ID, Role: "viewer"}, manager}, {{UserID: f.users[2].ID, Role: "viewer"}}, {{UserID: 999, Role: "manager"}}, {manager, manager}, {{UserID: f.users[2].ID, Role: "admin"}}} {
		wantErr(t, f.s.SetSharing(ctx, f.other, f.list.ID, grants), ErrInvalid)
	}
	for _, defaults := range [][]int64{{f.users[0].ID}, {f.users[1].ID}, {999}, {f.users[2].ID, f.users[2].ID}} {
		wantErr(t, f.s.SetUser(ctx, f.admin, f.users[1].ID, "owner", false, false, defaults), ErrInvalid)
	}
	must(t, f.s.SetUser(ctx, f.admin, f.users[1].ID, "owner", false, true, nil))
	must(t, f.s.SetSharing(ctx, f.owner, f.list.ID, nil))
	_, e := f.s.ListDetail(ctx, f.other, f.list.ID, page)
	wantErr(t, e, ErrMissing)
	// Defaults apply only at creation; metadata edits cannot alter old list grants.
	must(t, f.s.SetUser(ctx, f.admin, f.users[1].ID, "owner", false, true, []int64{f.users[3].ID}))
	sh, e := f.s.Sharing(ctx, f.owner, f.list.ID)
	must(t, e)
	if len(sh.Access) != 0 {
		t.Fatal(sh)
	}
	fresh, e := f.s.CreateList(ctx, f.owner, "New", "2030-12-25")
	must(t, e)
	must(t, f.s.SetUser(ctx, f.admin, f.users[1].ID, "owner", false, false, nil))
	sh, e = f.s.Sharing(ctx, f.owner, fresh.ID)
	must(t, e)
	if sh.CanEdit || len(sh.Access) != 1 {
		t.Fatal(sh)
	}
	*f.now = f.now.Add(24 * time.Hour)
	wantErr(t, f.s.SetSharing(ctx, f.other, fresh.ID, sh.Access), ErrArchived)
	sh, e = f.s.Sharing(ctx, f.other, fresh.ID)
	must(t, e)
	if sh.CanEdit {
		t.Fatal("archive editable")
	}
}

func TestDisableManagerGuard(t *testing.T) {
	for _, scenario := range []string{"sole manager", "archived", "other disabled", "other inactive", "other list", "other enabled", "owner can share"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			manager := f.users[3]
			switch scenario {
			case "archived":
				*f.now = f.now.Add(24 * time.Hour)
			case "other disabled", "other inactive", "other enabled":
				must(t, f.s.SetSharing(ctx, f.other, f.list.ID, []Access{{UserID: manager.ID, Role: "manager"}, {UserID: f.users[2].ID, Role: "manager"}}))
				if scenario == "other disabled" {
					must(t, f.s.SetUser(ctx, f.admin, f.users[2].ID, "member", true, false, nil))
				}
				if scenario == "other inactive" {
					must(t, f.s.SetSharing(ctx, f.other, f.list.ID, []Access{{UserID: manager.ID, Role: "manager"}}))
				}
			case "other list":
				must(t, f.s.SetUser(ctx, f.admin, f.users[4].ID, "outsider", false, false, []int64{f.users[2].ID}))
				_, err := f.s.CreateList(ctx, f.outsider, "Separate", "2030-12-25")
				must(t, err)
			case "owner can share":
				must(t, f.s.SetUser(ctx, f.admin, f.users[1].ID, "owner", false, true, nil))
			}
			before, err := f.s.Users(ctx, f.admin)
			must(t, err)
			beforeJSON, _ := json.Marshal(before)
			err = f.s.SetUser(ctx, f.admin, manager.ID, "Changed", true, true, []int64{f.users[1].ID})
			if scenario == "other enabled" || scenario == "owner can share" {
				must(t, err)
				_, err = f.s.Register(ctx, f.other, "manager")
				wantErr(t, err, ErrMissing)
			} else {
				wantErr(t, err, ErrInvalid)
				after, err := f.s.Users(ctx, f.admin)
				must(t, err)
				afterJSON, _ := json.Marshal(after)
				if string(beforeJSON) != string(afterJSON) {
					t.Fatal("rejected settings changed metadata")
				}
				_, err = f.s.Sharing(ctx, f.other, f.list.ID)
				must(t, err)
			}
			_, err = f.s.ListDetail(ctx, f.admin, f.list.ID, page)
			wantErr(t, err, ErrMissing)
		})
	}
}

func TestUnauthorizedAndMissingIndistinguishable(t *testing.T) {
	f := setup(t)
	i := add(t, f, "Wish")
	for _, archived := range []bool{false, true} {
		if archived {
			*f.now = f.now.Add(24 * time.Hour)
		}
		for _, v := range []Identity{f.outsider, f.admin} {
			for _, list := range []int64{f.list.ID, 999999} {
				for _, item := range []int64{i.ID, 99999} {
					_, e := f.s.ListDetail(ctx, v, list, Page{Limit: -1})
					wantErr(t, e, ErrMissing)
					_, e = f.s.Sharing(ctx, v, list)
					wantErr(t, e, ErrMissing)
					wantErr(t, f.s.SetSharing(ctx, v, list, []Access{{Role: "invalid"}}), ErrMissing)
					wantErr(t, f.s.Claim(ctx, v, list, item), ErrMissing)
					wantErr(t, f.s.ReleaseClaim(ctx, v, list, item), ErrMissing)
					wantErr(t, f.s.DeleteSurprise(ctx, v, list, item), ErrMissing)
					_, e = f.s.UpdateRequest(ctx, v, list, item, Request{})
					wantErr(t, e, ErrMissing)
					_, e = f.s.DeleteRequest(ctx, v, list, item)
					wantErr(t, e, ErrMissing)
					_, e = f.s.RestoreRequest(ctx, v, list, item)
					wantErr(t, e, ErrMissing)
					_, e = f.s.AddSurprise(ctx, v, list, Request{})
					wantErr(t, e, ErrMissing)
				}
			}
		}
	}
}

func TestSchemaV1Upgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	db, e := sql.Open("sqlite", path)
	must(t, e)
	_, e = db.Exec(schema)
	must(t, e)
	_, e = db.Exec(`INSERT INTO users VALUES(1,'https://login.example.invalid','owner','Child',0),(2,'https://login.example.invalid','member','Parent',0);
 INSERT INTO groups VALUES(1,'Legacy',0); INSERT INTO memberships VALUES(1,1,1),(1,2,1);
 INSERT INTO lists VALUES(1,1,'Legacy list','2030-12-25','2030-12-01','2030-12-01',1); INSERT INTO shares VALUES(1,1,1);
 INSERT INTO items VALUES(1,1,1,0,'Wish','','','','',1,1,'2030-12-01','2030-12-01',1,0);
 INSERT INTO claims VALUES(1,2,1); INSERT INTO coordination VALUES(1,1,2,'claim','2030-12-01');`)
	must(t, e)
	must(t, db.Close())
	s, e := Open(Config{Path: path, Timezone: "UTC", Clock: func() time.Time { return time.Date(2030, 12, 25, 0, 0, 0, 0, time.UTC) }})
	must(t, e)
	defer s.Close()
	v, e := s.ListDetail(ctx, Identity{"https://login.example.invalid", "owner"}, 1, page)
	must(t, e)
	if v.Owner.Count != 1 {
		t.Fatal(v)
	}
	_, e = s.ListDetail(ctx, Identity{"https://login.example.invalid", "member"}, 1, page)
	wantErr(t, e, ErrMissing)
	var version, claims, history, groups int
	must(t, s.db.QueryRow("PRAGMA user_version").Scan(&version))
	must(t, s.db.QueryRow("SELECT count(*) FROM claims").Scan(&claims))
	must(t, s.db.QueryRow("SELECT count(*) FROM coordination").Scan(&history))
	must(t, s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('groups','memberships','shares')").Scan(&groups))
	if version != 2 || claims != 1 || history != 1 || groups != 0 {
		t.Fatal(version, claims, history, groups)
	}
	var enabled bool
	must(t, s.db.QueryRow("SELECT can_share_own FROM users WHERE id=1").Scan(&enabled))
	if enabled {
		t.Fatal("migration grants sharing")
	}
}
