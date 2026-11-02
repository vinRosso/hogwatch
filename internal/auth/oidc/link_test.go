package oidc

import "testing"

func TestDecide(t *testing.T) {
	verified := Identity{Issuer: "i", Subject: "s", Email: "a@x.example", EmailVerified: true}
	unverified := Identity{Issuer: "i", Subject: "s", Email: "a@x.example"}
	noEmail := Identity{Issuer: "i", Subject: "s"}

	user := func(c Candidate) *Candidate { return &c }
	rows := []struct {
		name      string
		id        Identity
		bySubject *Candidate
		byEmail   *Candidate
		autolink  bool
		want      Decision
	}{
		// Rule 1: a linked subject logs in as that account, whatever the email says.
		{"linked", verified, user(Candidate{ID: 1}), nil, false, Decision{Kind: KindLogin, UserID: 1}},
		{"linked, disabled", verified, user(Candidate{ID: 1, Disabled: true}), nil, false,
			Decision{Kind: KindRefuse, Reason: ReasonDisabled}},
		{"linked, IdP email changed to another user's", verified, user(Candidate{ID: 1}), user(Candidate{ID: 2}), false,
			Decision{Kind: KindLogin, UserID: 1}},
		{"linked, unverified email of another user", unverified, user(Candidate{ID: 1}), user(Candidate{ID: 2}), false,
			Decision{Kind: KindLogin, UserID: 1}},
		{"linked admin_local is just logged in", verified, user(Candidate{ID: 1, AdminLocal: true}), nil, false,
			Decision{Kind: KindLogin, UserID: 1}},

		// Rule 2: a verified email links an unlinked, enabled account.
		{"verified match", verified, nil, user(Candidate{ID: 2}), false, Decision{Kind: KindLinkAndLogin, UserID: 2}},
		{"verified match, disabled", verified, nil, user(Candidate{ID: 2, Disabled: true}), false,
			Decision{Kind: KindRefuse, Reason: ReasonDisabled}},
		{"verified match, already linked to another subject", verified, nil, user(Candidate{ID: 2, Linked: true}), false,
			Decision{Kind: KindRefuse, Reason: ReasonAskAdmin}},
		{"verified match, linked and disabled", verified, nil, user(Candidate{ID: 2, Linked: true, Disabled: true}), false,
			Decision{Kind: KindRefuse, Reason: ReasonAskAdmin}},
		{"verified match, admin_local, autolink off", verified, nil, user(Candidate{ID: 2, AdminLocal: true}), false,
			Decision{Kind: KindRefuse, Reason: ReasonLinkFromProfile}},
		{"verified match, admin_local, autolink on", verified, nil, user(Candidate{ID: 2, AdminLocal: true}), true,
			Decision{Kind: KindLinkAndLogin, UserID: 2}},
		{"admin_local and disabled, autolink off: disabled wins", verified, nil, user(Candidate{ID: 2, AdminLocal: true, Disabled: true}), false,
			Decision{Kind: KindRefuse, Reason: ReasonDisabled}},
		{"admin_local and linked, autolink off: ask admin wins", verified, nil, user(Candidate{ID: 2, AdminLocal: true, Linked: true}), false,
			Decision{Kind: KindRefuse, Reason: ReasonAskAdmin}},
		{"autolink on does not override an existing link", verified, nil, user(Candidate{ID: 2, AdminLocal: true, Linked: true}), true,
			Decision{Kind: KindRefuse, Reason: ReasonAskAdmin}},
		{"autolink on does not override disabled", verified, nil, user(Candidate{ID: 2, AdminLocal: true, Disabled: true}), true,
			Decision{Kind: KindRefuse, Reason: ReasonDisabled}},

		// Rule 3: an unverified email never links, never creates a duplicate either.
		{"unverified match", unverified, nil, user(Candidate{ID: 2}), false, Decision{Kind: KindRefuse, Reason: ReasonAskAdmin}},
		{"unverified match, autolink on", unverified, nil, user(Candidate{ID: 2, AdminLocal: true}), true,
			Decision{Kind: KindRefuse, Reason: ReasonAskAdmin}},
		{"unverified match, disabled", unverified, nil, user(Candidate{ID: 2, Disabled: true}), false,
			Decision{Kind: KindRefuse, Reason: ReasonAskAdmin}},

		// Rule 4: nothing matches, create. The email is stored only when verified.
		{"new, verified", verified, nil, nil, false, Decision{Kind: KindCreate, StoreEmail: true}},
		{"new, unverified", unverified, nil, nil, false, Decision{Kind: KindCreate}},
		{"new, no email", noEmail, nil, nil, false, Decision{Kind: KindCreate}},

		// A caller that passes a match for an empty email must not link by it.
		{"no email but a match was passed", noEmail, nil, user(Candidate{ID: 2}), false, Decision{Kind: KindCreate}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := Decide(r.id, r.bySubject, r.byEmail, r.autolink); got != r.want {
				t.Fatalf("got %+v, want %+v", got, r.want)
			}
		})
	}
}

// EmailVerified false must never produce a link, whatever the candidate looks like.
func TestDecide_UnverifiedNeverLinks(t *testing.T) {
	for _, admin := range []bool{false, true} {
		for _, linked := range []bool{false, true} {
			for _, disabled := range []bool{false, true} {
				for _, autolink := range []bool{false, true} {
					id := Identity{Subject: "s", Email: "a@x.example"}
					d := Decide(id, nil, &Candidate{ID: 9, AdminLocal: admin, Linked: linked, Disabled: disabled}, autolink)
					if d.Kind == KindLinkAndLogin || d.Kind == KindLogin || d.Kind == KindCreate {
						t.Fatalf("admin=%v linked=%v disabled=%v autolink=%v: %+v", admin, linked, disabled, autolink, d)
					}
				}
			}
		}
	}
}
