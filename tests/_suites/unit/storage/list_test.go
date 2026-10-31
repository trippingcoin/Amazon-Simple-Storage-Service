package storage

import (
	"A3S/internal/objectstore"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestListObjectsPagination(t *testing.T) {
	s, _ := openTest(t, testOptions)
	bucket(t, s, "abc")
	bucket(t, s, "empty")
	for i := 19; i >= 0; i-- {
		put(t, s, "abc", fmt.Sprintf("file-%02d", i), "data")
	}
	put(t, s, "abc", "other", "excluded")
	put(t, s, "abc", "файл", "unicode")
	ctx := context.Background()
	opts := objectstore.ListOptions{Prefix: "file-", Limit: 3}
	var got []string
	for {
		page, err := s.ListObjects(ctx, "abc", opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Objects) > 3 {
			t.Fatal("unbounded page")
		}
		for _, o := range page.Objects {
			got = append(got, string(o.ID.Key))
			if o.Size != 4 {
				t.Fatal(o)
			}
		}
		if !page.HasMore {
			if page.NextAfter != "" {
				t.Fatal("terminal cursor")
			}
			break
		}
		if page.NextAfter <= opts.After {
			t.Fatal("cursor did not advance")
		}
		opts.After = page.NextAfter
	}
	want := make([]string, 20)
	for i := range want {
		want[i] = fmt.Sprintf("file-%02d", i)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys=%v", got)
	}
	// Cursor is a key, so removing the last item of an earlier page is safe.
	if err := s.DeleteObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "file-05"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListObjects(ctx, "abc", objectstore.ListOptions{Prefix: "file-", After: "file-05", Limit: 1})
	if err != nil || len(page.Objects) != 1 || page.Objects[0].ID.Key != "file-06" {
		t.Fatal(page, err)
	}
	page, err = s.ListObjects(ctx, "empty", objectstore.ListOptions{Limit: 1})
	if err != nil || len(page.Objects) != 0 || page.HasMore {
		t.Fatal(page, err)
	}
	page, err = s.ListObjects(ctx, "abc", objectstore.ListOptions{Prefix: "ф", Limit: 1})
	if err != nil || len(page.Objects) != 1 || page.Objects[0].ID.Key != "файл" || page.HasMore {
		t.Fatal(page, err)
	}
	if _, err = s.ListObjects(ctx, "missing", objectstore.ListOptions{Limit: 1}); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal(err)
	}
	for _, opts := range []objectstore.ListOptions{{Limit: 0}, {Limit: 1001}, {Prefix: "../", Limit: 1}, {After: "../file", Limit: 1}} {
		if _, err = s.ListObjects(ctx, "abc", opts); !errors.Is(err, objectstore.ErrInvalidList) {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.ListObjects(canceled, "abc", objectstore.ListOptions{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.Usage(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListObjects(ctx, "abc", objectstore.ListOptions{Limit: 1}); !errors.Is(err, objectstore.ErrClosed) {
		t.Fatal(err)
	}
	if _, err = s.Usage(ctx); !errors.Is(err, objectstore.ErrClosed) {
		t.Fatal(err)
	}
}
