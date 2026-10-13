package httpapi

import (
	"A3S/internal/objectstore"
	"encoding/xml"
	"net/http"
)

func bucketHandler(s Operations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("bucket")
		var err error
		switch r.Method {
		case http.MethodPut:
			err = s.CreateBucket(r.Context(), name)
		case http.MethodDelete:
			err = s.DeleteBucket(r.Context(), name)
		default:
			w.Header().Set("Allow", "PUT, DELETE")
			writeXMLError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			writeStorageError(w, err)
			return
		}
		writeXMLError(w, "Bucket operation completed", http.StatusOK)
	}
}

func objectHandler(s Operations, maxObjectBytes int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucket, key := r.PathValue("bucket"), r.PathValue("object")
		switch r.Method {
		case http.MethodGet:
			object, err := s.GetObject(r.Context(), bucket, key)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			// Keep the existing metadata GET contract; streaming download is Phase 3.
			writeXML(w, http.StatusOK, toObjectResponse(object))
		case http.MethodPut:
			if r.ContentLength > maxObjectBytes {
				writeStorageError(w, objectstore.ErrTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxObjectBytes)
			defer r.Body.Close()
			if err := s.PutObject(r.Context(), bucket, key, r.Body); err != nil {
				writeStorageError(w, err)
				return
			}
			writeXMLError(w, "Object created or overwritten successfully", http.StatusOK)
		case http.MethodDelete:
			if err := s.DeleteObject(r.Context(), bucket, key); err != nil {
				writeStorageError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			writeXMLError(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func rootHandler(s Operations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeXMLError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		buckets, err := s.ListBuckets(r.Context())
		if err != nil {
			writeStorageError(w, err)
			return
		}
		writeXML(w, http.StatusOK, struct {
			XMLName xml.Name         `xml:"Buckets"`
			Buckets []BucketResponse `xml:"Bucket"`
		}{Buckets: toBucketResponses(buckets)})
	}
}
