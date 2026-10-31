package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryRejectsUnsafeStateWithoutRemovingIt(t *testing.T) {
	for _, tc := range []struct{ name, path, kind, body string }{
		{"unknown staging", ".triple-s/unknown", "file", "evidence"},
		{"staging symlink", ".triple-s/upload-" + strings.Repeat("a", 32), "symlink", ""},
		{"bucket symlink", "linked", "symlink", ""},
		{"object symlink", "abc/linked", "symlink", ""},
		{"nested directory", "abc/nested", "dir", ""},
		{"invalid metadata", "abc/.bucket.json", "file", "{broken"},
		{"unexpected root file", "unexpected", "file", "evidence"},
		{"staged bucket unknown child", ".triple-s/bucket-" + strings.Repeat("b", 32) + "/unexpected", "file", "evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := openTest(t, testOptions)
			bucket(t, s, "abc")
			put(t, s, "abc", "file", "original")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, tc.path)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			switch tc.kind {
			case "file":
				err = os.WriteFile(target, []byte(tc.body), 0600)
			case "dir":
				err = os.Mkdir(target, 0700)
			case "symlink":
				err = os.Symlink(outside, target)
			}
			if err != nil {
				t.Fatal(err)
			}
			opened, err := Open(dir, testOptions)
			if err == nil {
				opened.Close()
				t.Fatal("accepted unsafe state")
			}
			if _, err = os.Lstat(target); err != nil {
				t.Fatal("removed forensic evidence", err)
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "untouched" {
				t.Fatal("modified symlink target")
			}
			disk(t, dir, "abc", "file", "original")
		})
	}
}
