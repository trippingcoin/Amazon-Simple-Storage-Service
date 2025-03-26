package error

import (
	"A3S/internal/models"
	"encoding/xml"
	"log"
	"net/http"
)

func WriteXMLError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(code)

	xmlResponse := models.XMLErrorResponse{
		Message: message,
		Code:    code,
	}

	xmlData, err := xml.MarshalIndent(xmlResponse, "", "  ")
	if err != nil {
		log.Printf("Error generating XML response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Write(xmlData)
}
