package oidc

// Candidate is an existing account Decide may log in or link.
type Candidate struct {
	ID                           int64
	AdminLocal, Disabled, Linked bool
}

// Kind is what the callback must do.
type Kind int

const (
	KindLogin        Kind = iota // log in as UserID
	KindLinkAndLogin             // link the identity to UserID, then log in
	KindCreate                   // create a new account
	KindRefuse                   // do nothing; Reason says why
)

// Reason is why a login was refused. The callback maps it to an error code.
type Reason int

const (
	ReasonNone            Reason = iota
	ReasonDisabled               // the account is disabled
	ReasonAskAdmin               // an account matches by email but may not be linked automatically
	ReasonLinkFromProfile        // a local admin must link from their profile
)

// Decision is the outcome of Decide.
type Decision struct {
	Kind   Kind
	UserID int64
	Reason Reason
	// StoreEmail is set for KindCreate when the new account may keep the IdP's
	// email: only a verified one, so an unverified claim can never be matched
	// later by the address's real owner.
	StoreEmail bool
}

// Decide applies the linking rules (security invariant 7). It is
// pure: the caller looks up the candidates inside one transaction and applies
// the decision in the same transaction.
//
//  1. An account already linked to (issuer, subject) logs in, if enabled.
//     Nothing else is consulted, so an email change at the IdP changes nothing.
//  2. A verified email that matches an unlinked, enabled account links it.
//     An account that is already linked is never relinked (a second IdP
//     account with the same email); a local admin links only from their profile
//     unless autolinkLocalAdmins is set.
//  3. An unverified email that matches an account is refused.
//  4. Otherwise a new account is created.
//
// bySubject is the account found by (issuer, subject); byEmail the one found
// by the identity's email, case-insensitively. byEmail is ignored when the
// identity has no email.
func Decide(id Identity, bySubject, byEmail *Candidate, autolinkLocalAdmins bool) Decision {
	if bySubject != nil {
		if bySubject.Disabled {
			return Decision{Kind: KindRefuse, Reason: ReasonDisabled}
		}
		return Decision{Kind: KindLogin, UserID: bySubject.ID}
	}
	if id.Email == "" {
		byEmail = nil
	}
	if byEmail != nil {
		switch {
		case !id.EmailVerified, byEmail.Linked:
			return Decision{Kind: KindRefuse, Reason: ReasonAskAdmin}
		case byEmail.Disabled:
			return Decision{Kind: KindRefuse, Reason: ReasonDisabled}
		case byEmail.AdminLocal && !autolinkLocalAdmins:
			return Decision{Kind: KindRefuse, Reason: ReasonLinkFromProfile}
		}
		return Decision{Kind: KindLinkAndLogin, UserID: byEmail.ID}
	}
	return Decision{Kind: KindCreate, StoreEmail: id.EmailVerified}
}
