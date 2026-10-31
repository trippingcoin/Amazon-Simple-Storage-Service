package storage

import (
	"A3S/internal/objectstore"
	"context"
	"errors"
	"slices"
	"testing"
)

func TestOfflineObjectKeys(t *testing.T) {
	s, _ := openTest(t, testOptions)
	bucket(t, s, "abc")
	for _, key := range []string{"z", "a", "middle"} {
		put(t, s, "abc", key, "data")
	}
	keys, err := s.ObjectKeys(context.Background(), "abc")
	if err != nil || !slices.Equal(keys, []objectstore.ObjectKey{"a", "middle", "z"}) {
		t.Fatalf("keys: %v %v", keys, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.ObjectKeys(ctx, "abc"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.ObjectKeys(context.Background(), "missing"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal(err)
	}
}
