// Package oidc is the OpenID Connect side of login: a PKCE code
// flow against one issuer, the pending-flow store, the linking rules and
// username derivation. It never touches the database.
package oidc

import (
	"slices"
	"strings"
)

const (
	// MaxEmail and MaxSubject are the longest email and subject kept; they
	// match the limits the admin API puts on the same fields.
	MaxEmail   = 320
	MaxSubject = 256
)

// Identity is what the identity provider vouched for. Every field comes from
// a verified ID token (or, for a missing email or groups claim, from UserInfo
// of the same subject).
type Identity struct {
	Issuer, Subject   string
	Email             string
	EmailVerified     bool // only ever true for the JSON boolean true
	PreferredUsername string
	Name              string
	Groups            []string
	GroupsAdmin       bool
}

// identityFrom reads claims decoded into generic JSON values. A claim of an
// unexpected type counts as absent, so odd tokens degrade to "unverified, not
// admin" and never to an error.
func identityFrom(issuer, subject string, claims map[string]any, groupsClaim, adminGroup string) Identity {
	id := Identity{Issuer: issuer, Subject: subject}
	id.Email = strings.TrimSpace(stringClaim(claims, "email"))
	if len(id.Email) > MaxEmail { // not an address worth storing or matching on
		id.Email = ""
	}
	id.PreferredUsername = stringClaim(claims, "preferred_username")
	id.Name = stringClaim(claims, "name")
	if v, ok := claims["email_verified"].(bool); ok && v && id.Email != "" {
		id.EmailVerified = true
	}
	if list, ok := claims[groupsClaim].([]any); ok {
		for _, g := range list {
			if s, ok := g.(string); ok {
				id.Groups = append(id.Groups, s)
			}
		}
	}
	id.GroupsAdmin = adminGroup != "" && slices.Contains(id.Groups, adminGroup)
	return id
}

func stringClaim(claims map[string]any, name string) string {
	s, _ := claims[name].(string)
	return s
}
