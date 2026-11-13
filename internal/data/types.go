// Package data owns persistence and the complete caller-view permission boundary.
// Identities supplied here must already have a verified issuer and subject.
package data

import (
	"errors"
	"time"
)

var (
	ErrMissing  = errors.New("not found")
	ErrArchived = errors.New("list is archived")
	ErrInvalid  = errors.New("invalid input")
	ErrConflict = errors.New("already claimed")
)

type Identity struct {
	Issuer  string
	Subject string
}
type Config struct {
	Path                string
	Timezone            string
	Clock               func() time.Time
	Administrators      []Identity
	Issuer              string
	Groups              []GroupMapping
	AdministratorGroups []string
}
type User struct {
	ID       int64
	Identity Identity
	Name     string
	Disabled bool
}
type Group struct {
	ID       int64
	Name     string
	Disabled bool
}
type Request struct {
	Name     string
	Link     string
	Price    string
	Note     string
	Image    string
	Quantity int
	Priority int
}

// OwnerItem deliberately cannot represent coordination data.
type OwnerItem struct {
	ID int64
	Request
	CreatedAt string
	UpdatedAt string
	Revision  int64
	Deleted   bool
}
type ViewerItem struct {
	OwnerItem
	CreatorID      int64
	BuyerName      string
	Surprise       bool
	BuyerID        *int64
	RemovedByOwner bool
}
type List struct {
	ID        int64
	OwnerID   int64
	Name      string
	Occasion  string
	Archived  bool
	CreatedAt string
	UpdatedAt string
	Revision  int64
}
type OwnerView struct {
	List   List
	Items  []OwnerItem
	Count  int
	Offset int
	Limit  int
}
type MemberView struct {
	List   List
	Items  []ViewerItem
	Count  int
	Offset int
	Limit  int
}

// Exactly one branch is populated. Owner serialization contains no member fields.
type View struct {
	Owner  *OwnerView  `json:"owner,omitempty"`
	Member *MemberView `json:"member,omitempty"`
}
type Summary struct {
	List  List
	Count int
}
type Page struct {
	Offset int
	Limit  int
}
