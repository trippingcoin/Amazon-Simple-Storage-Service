package httpapi

import (
	"A3S/internal/objectstore"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestWrappedErrorsHaveStablePublicResponses(t *testing.T) {
	for _, tc := range []struct {
		cause   error
		code    int
		message string
	}{
		{objectstore.ErrInvalid, 400, objectstore.ErrInvalid.Error()},
		{objectstore.ErrRecoveryRequired, 503, objectstore.ErrRecoveryRequired.Error()},
		{objectstore.ErrInvalidList, 400, objectstore.ErrInvalidList.Error()},
		{objectstore.ErrQuotaExceeded, 409, objectstore.ErrQuotaExceeded.Error()},
		{objectstore.ErrNotFound, 404, objectstore.ErrNotFound.Error()},
		{objectstore.ErrExists, 409, objectstore.ErrExists.Error()},
		{objectstore.ErrNotEmpty, 409, objectstore.ErrNotEmpty.Error()},
		{objectstore.ErrTooLarge, 413, "Object exceeds size limit"},
		{objectstore.ErrBusy, 503, "Storage operation capacity exhausted"},
		{context.Canceled, 408, "Request interrupted"},
		{context.DeadlineExceeded, 408, "Request interrupted"},
		{io.ErrUnexpectedEOF, 408, "Request interrupted"},
		{&http.MaxBytesError{Limit: 10}, 413, "Object exceeds size limit"},
		{&net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, 408, "Request timed out"},
		{syscall.ENOSPC, 500, "Storage operation failed"},
		{errors.New("private backend failure"), 500, "Storage operation failed"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeStorageError(w, fmt.Errorf("private/path/or/credential: %w", tc.cause))
			var response ErrorResponse
			if err := xml.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.code || response.Code != tc.code || response.Message != tc.message || response.XMLName.Local != "Error" {
				t.Fatalf("unexpected response: %d %+v", w.Code, response)
			}
			if strings.Contains(w.Body.String(), "private") {
				t.Fatal("backend detail exposed")
			}
			if errors.Is(tc.cause, objectstore.ErrBusy) && w.Header().Get("Retry-After") != "1" {
				t.Fatal("retry header missing")
			}
		})
	}
}

func TestXMLMarshalFailureKeepsErrorContract(t *testing.T) {
	w := httptest.NewRecorder()
	// Failure occurs after the serializer sees a private value. Neither that
	// partial value nor the marshal error should escape into the response.
	writeXML(w, 200, struct {
		Private     string
		Unsupported chan int
	}{Private: "private-secret", Unsupported: make(chan int)})
	var response ErrorResponse
	if err := xml.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal("error is not XML", err, w.Body.String())
	}
	if w.Code != 500 || response.XMLName.Local != "Error" || response.Code != 500 || response.Message != "Internal Server Error" {
		t.Fatal(w.Code, response)
	}
	if w.Header().Get("Content-Type") != "application/xml" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
		t.Fatal(w.Header())
	}
	if strings.Contains(w.Body.String(), "private-secret") || strings.Contains(w.Body.String(), "chan") {
		t.Fatal("serialization details leaked")
	}
}
func TestXMLUTF8EscapingAndByteLength(t *testing.T) {
	message := `Файл <report> & "quoted"`
	w := httptest.NewRecorder()
	writeXMLError(w, message, 400)
	var decoded ErrorResponse
	if err := xml.Unmarshal(w.Body.Bytes(), &decoded); err != nil || decoded.Message != message {
		t.Fatal(decoded, err)
	}
	if w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
		t.Fatal("length is not UTF-8 byte count")
	}
	if strings.Contains(w.Body.String(), "<report>") {
		t.Fatal("unescaped XML input")
	}
}
