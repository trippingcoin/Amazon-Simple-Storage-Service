package httpapi

import (
	"A3S/internal/access"
	"A3S/internal/objectstore"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
)

func writeXML(w http.ResponseWriter, status int, value any) {
	data, err := xml.MarshalIndent(value, "", "  ")
	if err != nil {
		// Do not leak marshal errors or change the API error representation.
		status = http.StatusInternalServerError
		data = []byte("<Error><Message>Internal Server Error</Message><Code>500</Code></Error>")
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data) // Request middleware records write failures.
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
	case errors.Is(err, access.ErrDenied):
		status = http.StatusForbidden
		message = "Access denied"
	case errors.Is(err, objectstore.ErrRecoveryRequired):
		status = http.StatusServiceUnavailable
		message = objectstore.ErrRecoveryRequired.Error()
	case errors.Is(err, objectstore.ErrQuotaExceeded):
		status = http.StatusConflict
		message = objectstore.ErrQuotaExceeded.Error()
	case errors.Is(err, objectstore.ErrInvalidList):
		status = http.StatusBadRequest
		message = objectstore.ErrInvalidList.Error()
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
		message = "Storage operation capacity exhausted"
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, io.ErrUnexpectedEOF):
		status = http.StatusRequestTimeout
		message = "Request interrupted"
	case errors.As(err, &netErr) && netErr.Timeout():
		status = http.StatusRequestTimeout
		message = "Request timed out"
	}
	// The request event records status and ID without exposing backend error text.
	writeXMLError(w, message, status)
}
