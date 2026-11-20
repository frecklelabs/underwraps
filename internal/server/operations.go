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
type accessArgument struct {
	User string `json:"user"`
	Role string `json:"role"`
}
type arguments struct {
	List            string           `json:"list,omitempty"`
	Item            string           `json:"item,omitempty"`
	Name            string           `json:"name,omitempty"`
	Date            string           `json:"date,omitempty"`
	Access          []accessArgument `json:"access,omitempty"`
	CanShareOwn     bool             `json:"can_share_own,omitempty"`
	DefaultManagers []string         `json:"default_managers,omitempty"`
	Request         data.Request     `json:"request,omitempty"`
	Archived        bool             `json:"archived,omitempty"`
	Offset          int              `json:"offset,omitempty"`
	Limit           int              `json:"limit,omitempty"`
	User            string           `json:"user,omitempty"`
	Disabled        bool             `json:"disabled,omitempty"`
}

var operationNames = []string{"lists", "detail", "deleted", "create_list", "update_list", "add_request", "update_request", "delete_request", "restore_request", "add_surprise", "claim", "release_claim", "delete_surprise", "users", "set_user", "people", "sharing", "set_sharing"}

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
	if (op == "users" || op == "set_user") && !p.Admin {
		return nil, data.ErrMissing
	}
	v := p.Identity
	var err error
	list, item := id(a.List), id(a.Item)
	page := data.Page{Offset: a.Offset, Limit: a.Limit}
	if page.Limit == 0 {
		page.Limit = 100
	}
	managers := []int64{}
	for _, m := range a.DefaultManagers {
		managers = append(managers, id(m))
	}
	switch op {
	case "lists":
		return s.store.Lists(ctx, v, a.Archived, page)
	case "detail":
		return s.store.ListDetail(ctx, v, list, page)
	case "deleted":
		return s.store.DeletedItems(ctx, v, list, page)
	case "create_list":
		return s.store.CreateList(ctx, v, a.Name, a.Date)
	case "update_list":
		return s.store.UpdateList(ctx, v, list, a.Name, a.Date)
	case "add_request", "update_request", "add_surprise":
		if _, err := s.store.ListDetail(ctx, v, list, data.Page{Limit: 1}); err != nil {
			return nil, err
		}
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
	case "people":
		return s.store.People(ctx, v)
	case "sharing":
		return s.store.Sharing(ctx, v, list)
	case "set_sharing":
		access := []data.Access{}
		for _, a := range a.Access {
			access = append(access, data.Access{UserID: id(a.User), Role: a.Role})
		}
		err = s.store.SetSharing(ctx, v, list, access)
	case "set_user":
		err = s.store.SetUser(ctx, v, id(a.User), a.Name, a.Disabled, a.CanShareOwn, managers)
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
				if k == "DefaultManagers" {
					if ids, ok := c.([]any); ok {
						for i, v := range ids {
							if n, ok := v.(json.Number); ok {
								ids[i] = n.String()
							}
						}
						x[k] = ids
						continue
					}
				}
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
