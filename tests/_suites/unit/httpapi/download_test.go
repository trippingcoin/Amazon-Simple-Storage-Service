package httpapi

import (
	"A3S/internal/objectstore"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingContent struct {
	*strings.Reader
	closed bool
}

func (f *failingContent) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (f *failingContent) Close() error             { f.closed = true; return nil }

type downloadOnly struct {
	Operations
	content objectstore.ReadSeekCloser
}

func (s downloadOnly) OpenObject(context.Context, string, string) (objectstore.Download, error) {
	return objectstore.Download{Object: objectstore.Object{Size: 4, ContentType: "text/plain", SHA256: strings.Repeat("a", 64)}, Content: s.content}, nil
}
func TestFailedDownloadAbortsAndCloses(t *testing.T) {
	f := &failingContent{Reader: strings.NewReader("data")}
	defer func() {
		recovered := recover()
		err, ok := recovered.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Errorf("expected abort, got %v", recovered)
		}
		if !f.closed {
			t.Error("stream leaked")
		}
	}()
	serveDownload(httptest.NewRecorder(), httptest.NewRequest("GET", "/abc/file", nil), downloadOnly{content: f}, "abc", "file")
}
