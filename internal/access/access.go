// Package access loads the startup-only identity registry. IDs are permanent:
// rotating tokens must never change or reassign a user's ID.
package access

import (
	"A3S/internal/objectstore"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
)

var ErrDenied = errors.New("access denied")

// Grant names a bucket in another permanent user's namespace. Permissions
// apply to the bucket name, including its future recreation by the owner.
type Grant struct {
	Owner      string `json:"owner"`
	Bucket     string `json:"bucket"`
	Permission string `json:"permission"`
}

type User struct {
	ID          string  `json:"id"`
	Role        string  `json:"role"`
	TokenSHA256 string  `json:"token_sha256"`
	Disabled    bool    `json:"disabled,omitempty"`
	Grants      []Grant `json:"grants,omitempty"`
}

var validID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func Validate(users []User) error {
	if len(users) > 1000 {
		return errors.New("too many users")
	}
	ids, hashes := map[string]bool{}, map[string]bool{}
	for _, u := range users {
		digest, err := hex.DecodeString(u.TokenSHA256)
		if !validID.MatchString(u.ID) || (u.Role != "admin" && u.Role != "user") || err != nil || len(digest) != 32 || hex.EncodeToString(digest) != u.TokenSHA256 || ids[u.ID] || hashes[u.TokenSHA256] {
			return errors.New("invalid or duplicate user configuration")
		}
		ids[u.ID], hashes[u.TokenSHA256] = true, true
	}
	total := 0
	for _, u := range users {
		seen := map[string]bool{}
		total += len(u.Grants)
		if len(u.Grants) > 256 || total > 4096 {
			return errors.New("too many bucket grants")
		}
		for _, g := range u.Grants {
			key := g.Owner + "\x00" + g.Bucket
			if u.Role != "user" || !ids[g.Owner] || g.Owner == u.ID || objectstore.BucketID(g.Bucket).Validate() != nil || (g.Permission != "read" && g.Permission != "write") || seen[key] {
				return errors.New("invalid or duplicate bucket grant")
			}
			seen[key] = true
		}
	}
	return nil
}

func Load(path string) ([]User, error) {
	// Reject symlinks and check the opened file again to avoid replacing a checked
	// file with a different inode between inspection and open.
	before, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("cannot read users file")
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return nil, errors.New("users file must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open users file")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm()&0077 != 0 || after.Size() > 1<<20 {
		return nil, errors.New("unsafe users file")
	}
	var doc struct {
		Version int    `json:"version"`
		Users   []User `json:"users"`
	}
	dec := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil || doc.Version != 1 || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid users file")
	}
	if err = Validate(doc.Users); err != nil {
		return nil, err
	}
	return doc.Users, nil
}

// Match compares every configured digest, including disabled entries, without
// logging credentials or exposing which account failed authentication.
func Match(users []User, token string) (User, bool) {
	supplied := sha256.Sum256([]byte(token))
	var result User
	found := false
	for _, u := range users {
		expected, _ := hex.DecodeString(u.TokenSHA256)
		match := subtle.ConstantTimeCompare(supplied[:], expected) == 1
		if match && !u.Disabled && len(token) >= 32 {
			result = u
			found = true
		}
	}
	return result, found
}

func Namespace(id string) string { sum := sha256.Sum256([]byte(id)); return hex.EncodeToString(sum[:]) }
