package oidc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func mustMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestIdentity_Claims(t *testing.T) {
	rows := []struct {
		name         string
		claims       string
		adminGroup   string
		wantVerified bool
		wantAdmin    bool
		wantGroups   []string
	}{
		{"verified bool", `{"email":"a@x","email_verified":true}`, "", true, false, nil},
		{"verified string", `{"email":"a@x","email_verified":"true"}`, "", false, false, nil},
		{"verified number", `{"email":"a@x","email_verified":1}`, "", false, false, nil},
		{"verified false", `{"email":"a@x","email_verified":false}`, "", false, false, nil},
		{"verified missing", `{"email":"a@x"}`, "", false, false, nil},
		{"verified null", `{"email":"a@x","email_verified":null}`, "", false, false, nil},
		{"groups list", `{"groups":["staff","admins"]}`, "admins", false, true, []string{"staff", "admins"}},
		{"groups other", `{"groups":["staff"]}`, "admins", false, false, []string{"staff"}},
		{"groups string", `{"groups":"admins"}`, "admins", false, false, nil},
		{"groups numbers", `{"groups":[1,2]}`, "admins", false, false, nil},
		{"groups mixed", `{"groups":[1,"admins",null]}`, "admins", false, true, []string{"admins"}},
		{"groups missing", `{}`, "admins", false, false, nil},
		{"groups object", `{"groups":{"admins":true}}`, "admins", false, false, nil},
		{"empty admin group never admin", `{"groups":["","admins"]}`, "", false, false, []string{"", "admins"}},
		{"verified without a usable email", `{"email":5,"email_verified":true}`, "", false, false, nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			id := identityFrom("https://idp", "sub-1", mustMap(t, r.claims), "groups", r.adminGroup)
			if id.EmailVerified != r.wantVerified || id.GroupsAdmin != r.wantAdmin {
				t.Errorf("verified=%v admin=%v, want %v %v", id.EmailVerified, id.GroupsAdmin, r.wantVerified, r.wantAdmin)
			}
			if !reflect.DeepEqual(id.Groups, r.wantGroups) {
				t.Errorf("groups = %#v, want %#v", id.Groups, r.wantGroups)
			}
			if id.Issuer != "https://idp" || id.Subject != "sub-1" {
				t.Errorf("issuer/subject = %q/%q", id.Issuer, id.Subject)
			}
		})
	}
}

func TestIdentity_TextClaims(t *testing.T) {
	id := identityFrom("i", "s", mustMap(t, `{"email":"A@X.example","preferred_username":"alice","name":"Alice A"}`), "groups", "")
	if id.Email != "A@X.example" || id.PreferredUsername != "alice" || id.Name != "Alice A" {
		t.Fatalf("got %+v", id)
	}
	// An address is stored and matched without surrounding whitespace.
	if id := identityFrom("i", "s", mustMap(t, `{"email":"  a@x.example  "}`), "groups", ""); id.Email != "a@x.example" {
		t.Fatalf("email = %q", id.Email)
	}
	// A non-string text claim is ignored, never a panic.
	id = identityFrom("i", "s", mustMap(t, `{"email":["a"],"preferred_username":7,"name":{}}`), "groups", "")
	if id.Email != "" || id.PreferredUsername != "" || id.Name != "" {
		t.Fatalf("non-string claims leaked: %+v", id)
	}
}
