package httpapi

import (
	"A3S/internal/objectstore"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
)

func writeXML(w http.ResponseWriter, status int, value any) {
	data, err := xml.MarshalIndent(value, "", "  ")
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err = w.Write(data); err != nil {
		log.Print("HTTP response write failed")
	}
}
func writeXMLError(w http.ResponseWriter, message string, status int) {
	writeXML(w, status, ErrorResponse{Message: message, Code: status})
}
func writeStorageError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "Storage operation failed"
	var maxErr *http.MaxBytesError
	var netErr net.Error
	switch {
	case errors.Is(err, objectstore.ErrInvalid):
		status = http.StatusBadRequest
		message = objectstore.ErrInvalid.Error()
	case errors.Is(err, objectstore.ErrNotFound):
		status = http.StatusNotFound
		message = objectstore.ErrNotFound.Error()
	case errors.Is(err, objectstore.ErrExists):
		status = http.StatusConflict
		message = objectstore.ErrExists.Error()
	case errors.Is(err, objectstore.ErrNotEmpty):
		status = http.StatusConflict
		message = objectstore.ErrNotEmpty.Error()
	case errors.Is(err, objectstore.ErrTooLarge), errors.As(err, &maxErr):
		status = http.StatusRequestEntityTooLarge
		message = "Object exceeds size limit"
	case errors.Is(err, objectstore.ErrBusy):
		status = http.StatusServiceUnavailable
		message = "Upload capacity exhausted"
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, io.ErrUnexpectedEOF):
		status = http.StatusRequestTimeout
		message = "Request interrupted"
	case errors.As(err, &netErr) && netErr.Timeout():
		status = http.StatusRequestTimeout
		message = "Request timed out"
	}
	if status == http.StatusInternalServerError {
		log.Printf("Storage operation failed: %v", err)
	}
	writeXMLError(w, message, status)
}
