package httpapi

import (
	"A3S/internal/objectstore"
	"context"
	"encoding/xml"
	"net/http/httptest"
	"testing"
	"time"
)

type getOnlyService struct {
	Operations
	get func(context.Context, string, string) (objectstore.Object, error)
}

func (s getOnlyService) GetObject(ctx context.Context, bucket, key string) (objectstore.Object, error) {
	return s.get(ctx, bucket, key)
}
func TestTransportUsesInjectedServiceAndPreservesWireIdentity(t *testing.T) {
	type requestKey struct{}
	called := 0
	var received context.Context
	api := getOnlyService{get: func(ctx context.Context, bucket, key string) (objectstore.Object, error) {
		called++
		received = ctx
		if ctx.Value(requestKey{}) != "request-42" {
			t.Fatal("request context value lost")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute {
			t.Fatal("request deadline missing")
		}
		return objectstore.Object{ID: objectstore.ObjectID{Bucket: objectstore.BucketID(bucket), Key: objectstore.ObjectKey(key)}, Size: 123, ContentType: "text/plain"}, nil
	}}
	token := "test-token-for-transport-only-123456789"
	h, err := New(api, Options{Token: token, RequestTimeout: time.Minute, MaxObjectBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/abc/report.txt?metadata=1", nil).WithContext(context.WithValue(context.Background(), requestKey{}, "request-42"))
	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, r)
	if unauthorized.Code != 401 || called != 0 {
		t.Fatal("unauthorized request reached service")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var object ObjectResponse
	if err := xml.Unmarshal(w.Body.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || called != 1 || object.ObjectKey != "data/abc/report.txt" || object.Size != 123 || object.XMLName.Local != "Object" {
		t.Fatalf("unexpected wire response: %+v", object)
	}
	if received.Err() != context.Canceled {
		t.Fatal("handler did not release request context")
	}
}
