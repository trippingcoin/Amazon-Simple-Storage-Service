package main

import (
	"A3S/internal/access"
	"A3S/internal/httpapi"
	"A3S/internal/observatory"
	"A3S/internal/service"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestObservatoryAdminPrivacyAndPolling(t *testing.T) {
	_, store, _ := testHandler(t, 1024)
	svc := service.New(store)
	m, e := observatory.New("local", localObservatory([]httpapi.Operations{svc}))
	if e != nil {
		t.Fatal(e)
	}
	h, e := httpapi.New(svc, httpapi.Options{Token: testToken, Observatory: m, RequestTimeout: time.Minute, MaxObjectBytes: 1024, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/-/observatory", "/-/observatory/app.js", "/-/observatory/style.css"} {
		w := request(h, "GET", path, "", "")
		requireStatus(t, w, 200)
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("unsafe shell headers")
		}
	}
	requireStatus(t, request(h, "GET", "/-/observatory/state", "", ""), 401)
	requireStatus(t, request(h, "GET", "/-/observatory/state?token="+testToken, "", ""), 401)
	requireStatus(t, request(h, "PUT", "/private-bucket", "", testToken), 200)
	requireStatus(t, request(h, "PUT", "/private-bucket/private-key?secret=query", "secret-payload", testToken), 200)
	m.Sample(context.Background())
	for i := 0; i < 4; i++ {
		w := request(h, "GET", "/-/observatory/state", "", testToken)
		requireStatus(t, w, 200)
		var r observatory.Report
		if e = json.Unmarshal(w.Body.Bytes(), &r); e != nil {
			t.Fatal(e)
		}
		if r.Nodes[0].Requests != 2 || r.Nodes[0].UploadBytes != 14 || r.Storage.Bytes != 14 {
			t.Fatalf("poll contamination %+v", r)
		}
		for _, secret := range []string{testToken, "private-bucket", "private-key", "secret-payload", "query"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("telemetry leaked", secret)
			}
		}
	}
	requireStatus(t, request(h, "POST", "/-/observatory/state", "", testToken), 405)
	w := request(h, "HEAD", "/-/observatory/state", "", testToken)
	requireStatus(t, w, 200)
	if w.Body.Len() != 0 {
		t.Fatal("HEAD has body")
	}
}
func TestObservatoryRejectsTenantAndSharedAccess(t *testing.T) {
	_, store, _ := testHandler(t, 1024)
	svc := service.New(store)
	user := identity("alice", aliceToken, "user")
	h, e := httpapi.New(svc, httpapi.Options{Token: testToken, Users: []access.User{user}, Tenants: map[string]httpapi.Operations{"alice": svc}, RequestTimeout: time.Minute, MaxObjectBytes: 1024, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if e != nil {
		t.Fatal(e)
	}
	requireStatus(t, request(h, "GET", "/-/observatory/state", "", aliceToken), 403)
	r := httptest.NewRequest("GET", "/-/observatory/state", nil)
	r.Header.Add("Authorization", "Bearer "+testToken)
	r.Header.Add("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireStatus(t, w, 401)
}
