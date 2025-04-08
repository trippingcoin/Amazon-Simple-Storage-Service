package bucketHandl

import (
	"A3S/internal/csv"
	"A3S/internal/models"
	"A3S/internal/utils"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

func BucketHandler(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	switch r.Method {
	case http.MethodPut:
		PutBucket(w, r, s)
	case http.MethodDelete:
		DeleteBucket(w, r, s)
	default:
		utils.WriteXMLError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func PutBucket(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	bucket := r.PathValue("bucket")

	statusCode, err := ValidateBucketName(bucket)
	if err != nil {
		utils.WriteXMLError(w, fmt.Sprintf("Invalid bucket name: %v", err), statusCode)
		return
	}

	dataDir := "data"

	if _, err := os.Stat(*utils.Dir); os.IsNotExist(err) {
		data := "data"
		os.Mkdir(data, 0o777)
	}

	BucketDir := filepath.Join(dataDir, bucket)

	if _, err := os.Stat(BucketDir); !os.IsNotExist(err) {
		utils.WriteXMLError(w, "Directory with this name already exists", http.StatusConflict)
		return
	}

	err = os.Mkdir(BucketDir, 0o755)
	if err != nil {
		utils.WriteXMLError(w, "Error creating directory", http.StatusInternalServerError)
		return
	}

	// creating new bucket
	newBucket := &models.Bucket{
		Name:         bucket,
		CreationTime: time.Now(),
		LastModified: time.Now(),
		Status:       "Marked for deletion",
	}

	// adding bucket to buckets array
	s.Buckets = append(s.Buckets, *newBucket)

	log.Println("Current buckets in storage:")
	for _, b := range s.Buckets {
		log.Printf("Bucket: %s", b.Name)
	}

	csv.CSVBucketWriter(newBucket)

	utils.WriteXMLError(w, fmt.Sprintf("Bucket '%s' created successfully!", bucket), http.StatusOK)
}

func DeleteBucket(w http.ResponseWriter, r *http.Request, s *models.Storage) {
	bucketName := r.PathValue("bucket")

	log.Printf("Requested bucket for deletion: %s", bucketName)

	// searching bucket by name
	var bucket *models.Bucket
	bucketIndex := -1
	for i, b := range s.Buckets {
		if b.Name == bucketName {
			bucket = &s.Buckets[i]
			bucketIndex = i
			break
		}
	}

	if bucket == nil {
		utils.WriteXMLError(w, "Bucket not found", http.StatusNotFound)
		return
	}

	if bucket.Status == "Activ" {
		log.Printf("Bucket '%s' is active and cannot be deleted", bucketName)
		utils.WriteXMLError(w, "Bucket has object and cannot be deleted", http.StatusForbidden)
		return
	}

	if bucket.Status != "Marked for deletion" {
		log.Printf("Bucket '%s' is not marked for deletion", bucketName)
		utils.WriteXMLError(w, "Bucket is not empty", http.StatusConflict)
		return
	}

	err := os.RemoveAll(filepath.Join("data", bucketName))
	if err != nil {
		utils.WriteXMLError(w, "Failed to delete bucket directory", http.StatusInternalServerError)
		return
	}

	// deleteing bucket from storage
	s.Buckets = append(s.Buckets[:bucketIndex], s.Buckets[bucketIndex+1:]...)

	csv.CSVDBucketDelete(bucket)
	utils.WriteXMLError(w, fmt.Sprintf("Bucket '%s' deleted successfully", bucketName), http.StatusOK)
	log.Printf("Bucket '%s' deleted successfully", bucketName)
}

func CreateBucketHandler(s *models.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		BucketHandler(w, r, s)
	}
}

func ValidateBucketName(bucketName string) (int, error) {
	if len(bucketName) < 3 || len(bucketName) > 63 {
		return http.StatusBadRequest, errors.New("bucket name must be between 3 and 63 characters")
	}

	validBucketName := regexp.MustCompile(`^[a-z0-9]([a-z0-9\-\.]{1,61}[a-z0-9])?$`)

	if !validBucketName.MatchString(bucketName) {
		return http.StatusBadRequest, errors.New("bucket name must only contain lowercase letters, numbers, hyphens, and periods")
	}

	if net.ParseIP(bucketName) != nil {
		return http.StatusBadRequest, errors.New("bucket name must not be formatted as an IP address")
	}

	return 0, nil
}

func UpdateBucketStatus(bucket *models.Bucket, s *models.Storage) {
	isEmpty := true

	for _, object := range s.Object {
		if object.ObjectKey == bucket.Name {
			isEmpty = false
			break
		}
	}

	if isEmpty {
		bucket.Status = "Marked for deletion"
	} else {
		bucket.Status = "Activ"
	}
}
