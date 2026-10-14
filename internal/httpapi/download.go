package httpapi

import (
	"io"
	"mime"
	"net/http"
)

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w headWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func serveDownload(w http.ResponseWriter, r *http.Request, s Operations, bucket, key string) {
	download, err := s.OpenObject(r.Context(), bucket, key)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer download.Content.Close()
	m := download.Object
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": key}))
	w.Header().Set("ETag", `"`+m.SHA256+`"`)
	w.Header().Set("X-Checksum-SHA256", m.SHA256)
	reader := &checkedReader{ReadSeeker: download.Content, size: m.Size}
	writer := &checkedWriter{ResponseWriter: w}
	http.ServeContent(writer, r, key, m.LastModified, reader)
	// ServeContent cannot report copy errors after headers. Abort the connection
	// instead of appending XML to a partially transmitted object.
	if reader.failed || writer.failed {
		panic(http.ErrAbortHandler)
	}
}

type checkedReader struct {
	io.ReadSeeker
	size, pos int64
	failed    bool
}

func (r *checkedReader) Read(p []byte) (int, error) {
	n, err := r.ReadSeeker.Read(p)
	r.pos += int64(n)
	if err != nil && (err != io.EOF || r.pos < r.size) {
		r.failed = true
	}
	return n, err
}
func (r *checkedReader) Seek(n int64, w int) (int64, error) {
	pos, err := r.ReadSeeker.Seek(n, w)
	if err == nil {
		r.pos = pos
	}
	return pos, err
}

type checkedWriter struct {
	http.ResponseWriter
	failed bool
}

func (w *checkedWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err != nil || n != len(p) {
		w.failed = true
	}
	return n, err
}
func (w *checkedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
