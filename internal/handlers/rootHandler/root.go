package root

import (
	"A3S/internal/models"
	"A3S/internal/utils"
	"encoding/xml"
	"net/http"
)

func RootHandler(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	switch r.Method {
	case http.MethodGet:
		GetRoot(w, r, s)
	default:
		utils.WriteXMLError(w, "method not founded", http.StatusMethodNotAllowed)
	}
}

func GetRoot(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// response header XML
	w.Header().Set("Content-Type", "application/xml")

	if s.Buckets == nil {
		utils.WriteXMLError(w, "data is empty , plese create bucket", http.StatusInternalServerError)
	}

	// buckets list to XML
	xmlData, err := xml.MarshalIndent(s.Buckets, "", "  ")
	if err != nil {
		http.Error(w, "Failed to generate XML", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(xmlData)
}

func CreateRootHandler(s *models.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		RootHandler(w, r, s)
	}
}
