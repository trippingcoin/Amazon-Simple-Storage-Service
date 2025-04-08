package objectHandl

import (
	"A3S/internal/csv"
	bucketHandl "A3S/internal/handlers/bucketHandler"
	"A3S/internal/models"
	"A3S/internal/utils"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func ObjectHandler(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	switch r.Method {
	case http.MethodGet:
		GetObject(w, r, s)
	case http.MethodPut:
		PutObject(w, r, s)
	case http.MethodDelete:
		DeleteObject(w, r, s)
	default:
		utils.WriteXMLError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func GetObject(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	if r.Method != http.MethodGet {
		utils.WriteXMLError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bucketName := r.PathValue("bucket")
	objectKey := r.PathValue("object")
	objectPath := filepath.Join("data", bucketName, objectKey)

	// searching bucket
	var bucket *models.Bucket
	for _, b := range s.Buckets {
		if b.Name == bucketName {
			bucket = &b
			break
		}
	}
	if bucket == nil {
		utils.WriteXMLError(w, "Bucket not found", http.StatusNotFound)
		return
	}

	// searching object
	var object *models.Object
	for _, o := range s.Object {
		if o.ObjectKey == objectPath {
			object = &o
			break
		}
	}
	if object == nil {
		utils.WriteXMLError(w, "Object not found", http.StatusNotFound)
		return
	}

	// adding header Content-Type for XML-response
	w.Header().Set("Content-Type", "application/xml")
	xmlData, err := xml.MarshalIndent(object, "", "  ")
	if err != nil {
		utils.WriteXMLError(w, "Failed to generate XML", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(xmlData)
}

func PutObject(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	object := r.PathValue("object")
	bucket := r.PathValue("bucket")

	bucketDir := filepath.Join("data", bucket)
	objectPath := filepath.Join(bucketDir, object)

	if _, err := os.Stat(bucketDir); os.IsNotExist(err) {
		utils.WriteXMLError(w, "Bucket directory not found", http.StatusConflict)
		return
	}

	// checking existing object
	objectIndex := -1
	for i, o := range s.Object {
		if o.ObjectKey == objectPath {
			objectIndex = i
			break
		}
	}

	// deleteing object form list CSV and storage
	if objectIndex != -1 {
		csv.CSVDeleteObject(&s.Object[objectIndex], bucket)
		s.Object = append(s.Object[:objectIndex], s.Object[objectIndex+1:]...)
	}

	// Creating new file object
	file, err := os.Create(objectPath)
	if err != nil {
		utils.WriteXMLError(w, "Error creating or overwriting file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// writing data from request
	bytesWritten, err := io.Copy(file, r.Body)
	if err != nil {
		utils.WriteXMLError(w, "Error saving file data", http.StatusInternalServerError)
		return
	}

	// getting file info
	fileInfo, err := file.Stat()
	if err != nil {
		utils.WriteXMLError(w, "Error obtaining file information", http.StatusInternalServerError)
		return
	}

	var objectContentType string
	if bytesWritten > 0 {
		// Reading 512 bytes
		file.Seek(0, io.SeekStart)
		buffer := make([]byte, 512)
		if _, err := file.Read(buffer); err != nil {
			utils.WriteXMLError(w, "Error reading file for content type detection", http.StatusInternalServerError)
			return
		}
		objectContentType = http.DetectContentType(buffer)
	}

	// creating new object and saving it in storage
	newObject := &models.Object{
		ObjectKey:    objectPath,
		Size:         int(fileInfo.Size()),
		ContentType:  objectContentType,
		LastModified: time.Now(),
	}

	s.Object = append(s.Object, *newObject)
	csv.CSVObjectWriter(newObject, bucket)

	// Refreshing bucket data
	for i, b := range s.Buckets {
		if b.Name == bucket {
			s.Buckets[i].LastModified = time.Now()
			s.Buckets[i].Status = "Active"
			csv.CSVUpdateBucketMetaData(&s.Buckets[i])
			break
		}
	}

	utils.WriteXMLError(w, fmt.Sprintf("Object '%s' created or overwritten successfully!", object), http.StatusOK)
}

func DeleteObject(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	if r.Method != http.MethodDelete {
		utils.WriteXMLError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bucketName := r.PathValue("bucket")
	objectKey := r.PathValue("object")

	filePath := filepath.Join("data", bucketName, objectKey)
	log.Printf("Attempting to delete file at path: %s", filePath)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("File not found at path: %s", filePath)
		utils.WriteXMLError(w, "Object file not found", http.StatusNotFound)
		return
	}

	err := os.Remove(filePath)
	if err != nil {
		log.Printf("Error while deleting file: %v", err)
		utils.WriteXMLError(w, "Failed to delete object file", http.StatusInternalServerError)
		return
	}
	log.Printf("Object file '%s' deleted successfully", filePath)

	// delete object from storage
	objectIndex := -1
	for i, obj := range s.Object {
		if obj.ObjectKey == filePath {
			objectIndex = i
			break
		}
	}

	// 404 error
	if objectIndex == -1 {
		log.Printf("Object not found in storage: %s", objectKey)
		utils.WriteXMLError(w, "Object not found in storage", http.StatusNotFound)
		return
	}

	// delete object from CSV
	csv.CSVObjectWriter(&s.Object[objectIndex], bucketName)
	s.Object = append(s.Object[:objectIndex], s.Object[objectIndex+1:]...)
	log.Printf("Object '%s' removed from memory storage", objectKey)

	// Refreshing bucket data
	var bucket *models.Bucket
	for i, b := range s.Buckets {
		if b.Name == bucketName {
			bucket = &s.Buckets[i]
			break
		}
	}

	// if Bucket doesn't found refreshing status
	if bucket != nil {
		log.Printf("Updating status of bucket: %s", bucket.Name)
		bucketHandl.UpdateBucketStatus(bucket, s)
		csv.CSVUpdateBucketMetaData(bucket)
	} else {
		log.Printf("Bucket '%s' not found for status update", bucketName)
	}

	w.WriteHeader(http.StatusNoContent)
}

func CreateObjectHandler(s *models.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ObjectHandler(w, r, s)
	}
}
