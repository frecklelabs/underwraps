package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/frecklelabs/underwraps/internal/data"
)

// All transports dispatch the same typed, caller-scoped operations. IDs travel as
// decimal strings because random item IDs exceed JavaScript's exact integer range.
type arguments struct {
	List     string       `json:"list,omitempty"`
	Item     string       `json:"item,omitempty"`
	Name     string       `json:"name,omitempty"`
	Date     string       `json:"date,omitempty"`
	Groups   []string     `json:"groups,omitempty"`
	Request  data.Request `json:"request,omitempty"`
	Archived bool         `json:"archived,omitempty"`
	Offset   int          `json:"offset,omitempty"`
	Limit    int          `json:"limit,omitempty"`
	User     string       `json:"user,omitempty"`
	Group    string       `json:"group,omitempty"`
	Disabled bool         `json:"disabled,omitempty"`
	Active   bool         `json:"active,omitempty"`
}

var operationNames = []string{"lists", "detail", "deleted", "create_list", "update_list", "add_request", "update_request", "delete_request", "restore_request", "add_surprise", "claim", "release_claim", "delete_surprise", "users", "groups", "set_user", "create_group", "set_group", "set_membership"}

func decode(raw []byte, a *arguments) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(a); err != nil {
		return data.ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return data.ErrInvalid
	}
	return nil
}
func id(raw string) int64 {
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n <= 0 {
		return 0
	}
	return n
}
func safeURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && !strings.ContainsAny(raw, "\r\n\x00")
}
func validRequest(r data.Request) bool {
	return len(r.Name) <= 300 && len(r.Link) <= 2048 && len(r.Image) <= 2048 && len(r.Note) <= 10000 && len(r.Price) <= 100 && r.Priority <= 1000000 && safeURL(r.Link) && safeURL(r.Image)
}
func (s *Server) operate(ctx context.Context, p principal, op string, a arguments) (any, error) {
	if (op == "users" || op == "groups" || op == "create_group" || strings.HasPrefix(op, "set_")) && !p.Admin {
		return nil, data.ErrMissing
	}
	v := p.Identity
	var err error
	list, item := id(a.List), id(a.Item)
	page := data.Page{Offset: a.Offset, Limit: a.Limit}
	if page.Limit == 0 {
		page.Limit = 100
	}
	groups := []int64{}
	for _, g := range a.Groups {
		groups = append(groups, id(g))
	}
	switch op {
	case "lists":
		return s.store.Lists(ctx, v, a.Archived, page)
	case "detail":
		return s.store.ListDetail(ctx, v, list, page)
	case "deleted":
		return s.store.DeletedItems(ctx, v, list, page)
	case "create_list":
		return s.store.CreateList(ctx, v, a.Name, a.Date, groups)
	case "update_list":
		return s.store.UpdateList(ctx, v, list, a.Name, a.Date, groups)
	case "add_request", "update_request", "add_surprise":
		if !validRequest(a.Request) {
			return nil, data.ErrInvalid
		}
		switch op {
		case "add_request":
			return s.store.AddRequest(ctx, v, list, a.Request)
		case "update_request":
			return s.store.UpdateRequest(ctx, v, list, item, a.Request)
		case "add_surprise":
			_, err = s.store.AddSurprise(ctx, v, list, a.Request)
		}
	case "delete_request":
		return s.store.DeleteRequest(ctx, v, list, item)
	case "restore_request":
		return s.store.RestoreRequest(ctx, v, list, item)
	case "claim":
		err = s.store.Claim(ctx, v, list, item)
	case "release_claim":
		err = s.store.ReleaseClaim(ctx, v, list, item)
	case "delete_surprise":
		err = s.store.DeleteSurprise(ctx, v, list, item)
	case "users":
		return s.store.Users(ctx, v)
	case "groups":
		return s.store.Groups(ctx, v)
	case "set_user":
		err = s.store.SetUser(ctx, v, id(a.User), a.Name, a.Disabled)
	case "create_group":
		return s.store.CreateGroup(ctx, v, a.Name)
	case "set_group":
		err = s.store.SetGroup(ctx, v, id(a.Group), a.Name, a.Disabled)
	case "set_membership":
		err = s.store.SetMembership(ctx, v, id(a.Group), id(a.User), a.Active)
	default:
		return nil, data.ErrMissing
	}
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(op, "set_") {
		return struct{}{}, nil
	}
	return s.store.ListDetail(ctx, v, list, page)
}
func publicError(err error) (int, string) {
	switch {
	case errors.Is(err, data.ErrMissing):
		return 404, "not found"
	case errors.Is(err, data.ErrInvalid):
		return 400, "invalid input"
	case errors.Is(err, data.ErrArchived):
		return 409, "list is archived"
	case errors.Is(err, data.ErrConflict):
		return 409, "already claimed"
	default:
		return 500, "operation failed"
	}
}
func wire(value any) []byte {
	b, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"error":"encoding failed"}`)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out any
	if d.Decode(&out) != nil {
		return []byte(`{}`)
	}
	if n, ok := out.(json.Number); ok {
		out = n.String()
	}
	var convert func(any) any
	convert = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if strings.HasSuffix(k, "ID") {
					if n, ok := c.(json.Number); ok {
						x[k] = n.String()
						continue
					}
				}
				x[k] = convert(c)
			}
		case []any:
			for i, c := range x {
				x[i] = convert(c)
			}
		}
		return v
	}
	b, _ = json.Marshal(convert(out))
	return b
}
