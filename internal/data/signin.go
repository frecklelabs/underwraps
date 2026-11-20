package data

import "context"

type Account struct {
	User  User
	Admin bool
}

// SignIn gates both transports on the required verified group. Group claims
// never grant list visibility; disabled accounts cannot reactivate themselves.
func (s *Store) SignIn(ctx context.Context, v Identity, name string, claims []string) (Account, error) {
	if v.Issuer != s.issuer || !validIdentity(v) || s.requiredGroup == "" {
		return Account{}, ErrInvalid
	}
	present := map[string]bool{}
	for _, c := range claims {
		present[c] = true
	}
	if !present[s.requiredGroup] {
		return Account{}, ErrMissing
	}
	u, err := s.Register(ctx, v, name)
	if err != nil {
		return Account{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	admin := s.staticAdmins[v]
	for _, g := range s.adminGroups {
		admin = admin || present[g]
	}
	s.admins[v] = admin
	return Account{User: u, Admin: admin}, nil
}
