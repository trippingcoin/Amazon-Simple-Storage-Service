package httpapi

import (
	"A3S/internal/objectstore"
	"encoding/xml"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type listEntry struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	ContentType  string    `xml:"ContentType"`
	LastModified time.Time `xml:"LastModified"`
}
type listResult struct {
	XMLName     xml.Name    `xml:"ListObjectsResult"`
	Bucket      string      `xml:"Bucket"`
	Prefix      string      `xml:"Prefix"`
	After       string      `xml:"After"`
	Limit       int         `xml:"Limit"`
	IsTruncated bool        `xml:"IsTruncated"`
	NextAfter   string      `xml:"NextAfter,omitempty"`
	Objects     []listEntry `xml:"Contents"`
}

func listObjects(w http.ResponseWriter, r *http.Request, s Operations, bucket string) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeStorageError(w, objectstore.ErrInvalidList)
		return
	}
	for key, values := range query {
		if (key != "prefix" && key != "after" && key != "limit") || len(values) != 1 {
			writeStorageError(w, objectstore.ErrInvalidList)
			return
		}
	}
	opts := objectstore.ListOptions{Prefix: query.Get("prefix"), After: objectstore.ObjectKey(query.Get("after")), Limit: 100}
	if query.Has("limit") {
		opts.Limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil {
			writeStorageError(w, objectstore.ErrInvalidList)
			return
		}
	}
	if err = opts.Validate(); err != nil {
		writeStorageError(w, err)
		return
	}
	page, err := s.ListObjects(r.Context(), bucket, opts)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	response := listResult{Bucket: bucket, Prefix: opts.Prefix, After: string(opts.After), Limit: opts.Limit, IsTruncated: page.HasMore, NextAfter: string(page.NextAfter)}
	for _, o := range page.Objects {
		response.Objects = append(response.Objects, listEntry{Key: string(o.ID.Key), Size: o.Size, ContentType: o.ContentType, LastModified: o.LastModified})
	}
	writeXML(w, http.StatusOK, response)
}
func usageHandler(s Operations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			writeXMLError(w, "Method not allowed", 405)
			return
		}
		u, err := s.Usage(r.Context())
		if err != nil {
			writeStorageError(w, err)
			return
		}
		writeXML(w, 200, struct {
			XMLName         xml.Name `xml:"StorageUsage"`
			StoredBytes     int64
			Objects         int64
			Buckets         int
			MaxStorageBytes int64
			MaxObjects      int64
		}{StoredBytes: u.StoredBytes, Objects: u.Objects, Buckets: u.Buckets, MaxStorageBytes: u.MaxStorageBytes, MaxObjects: u.MaxObjects})
	}
}
