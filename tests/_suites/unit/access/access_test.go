package access

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testUser() User {
	h := sha256.Sum256([]byte(strings.Repeat("a", 32)))
	return User{ID: "alice", Role: "user", TokenSHA256: hex.EncodeToString(h[:])}
}
func TestRegistryValidation(t *testing.T) {
	u := testUser()
	for _, mutate := range []func(*User){func(u *User) { u.ID = "../escape" }, func(u *User) { u.ID = "Alice" }, func(u *User) { u.Role = "owner" }, func(u *User) { u.TokenSHA256 = "invalid" }} {
		bad := u
		mutate(&bad)
		if Validate([]User{bad}) == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	if Validate([]User{u, u}) == nil {
		t.Fatal("duplicate accepted")
	}
	other := u
	other.ID = "bob"
	if Validate([]User{u, other}) == nil {
		t.Fatal("duplicate digest accepted")
	}
	if _, ok := Match([]User{u}, strings.Repeat("a", 32)); !ok {
		t.Fatal("valid token rejected")
	}
	u.Disabled = true
	if _, ok := Match([]User{u}, strings.Repeat("a", 32)); ok {
		t.Fatal("disabled token accepted")
	}
}
func TestLoadPrivateStrictRegistry(t *testing.T) {
	u := testUser()
	body := `{"version":1,"users":[{"id":"alice","role":"user","token_sha256":"` + u.TokenSHA256 + `"}]}`
	path := filepath.Join(t.TempDir(), "users.json")
	for _, bad := range []string{body + `{}`, strings.Replace(body, `"version":1`, `"version":2`, 1), strings.Replace(body, `"role"`, `"unknown"`, 1), `{`} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("malformed registry accepted")
		}
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if users, err := Load(path); err != nil || len(users) != 1 {
		t.Fatal(err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("public registry accepted")
	}
}

func TestGrantValidation(t *testing.T) {
	alice := testUser()
	bob := testUser()
	bob.ID = "bob"
	hash := sha256.Sum256([]byte(strings.Repeat("b", 32)))
	bob.TokenSHA256 = hex.EncodeToString(hash[:])
	grant := Grant{Owner: "alice", Bucket: "shared", Permission: "read"}
	bob.Grants = []Grant{grant}
	if err := Validate([]User{alice, bob}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Grant{{Owner: "missing", Bucket: "shared", Permission: "read"}, {Owner: "bob", Bucket: "shared", Permission: "read"}, {Owner: "alice", Bucket: "../x", Permission: "read"}, {Owner: "alice", Bucket: "shared", Permission: "admin"}} {
		bob.Grants = []Grant{bad}
		if Validate([]User{alice, bob}) == nil {
			t.Fatal("invalid grant accepted")
		}
	}
	bob.Grants = []Grant{grant, grant}
	if Validate([]User{alice, bob}) == nil {
		t.Fatal("duplicate grants accepted")
	}
	bob.Grants = []Grant{grant}
	bob.Role = "admin"
	if Validate([]User{alice, bob}) == nil {
		t.Fatal("ambiguous admin grants accepted")
	}
}
