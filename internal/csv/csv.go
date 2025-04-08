package csv

import (
	"A3S/internal/models"
	"encoding/csv"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func CSVBucketWriter(bucket *models.Bucket) {
	dataDir := "data"
	metaFilePath := dataDir + "/BucketMetaData.csv"

	// open file to write data
	metaFile, err := os.OpenFile(metaFilePath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		log.Fatal("error: ", err)
	}

	// get file info
	info, err := metaFile.Stat()
	if err != nil {
		log.Fatal("error: ", err)
	}
	// Writeing header to CSV if empty
	if info.Size() == 0 {
		_, err = metaFile.WriteString("Name,CreationTime,LastModifiedTime,Status\n")
		if err != nil {
			log.Fatal("Can't create Header in CSV:", err)
		}
	}

	defer metaFile.Close()

	writer := csv.NewWriter(metaFile)
	defer writer.Flush()
	// replacing to the end
	_, err = metaFile.Seek(0, io.SeekEnd)
	if err != nil {
		log.Fatal("error:", err)
	}

	// Creating a row with info about bucket
	row := []string{
		bucket.Name,
		bucket.CreationTime.Format(time.RFC3339),
		bucket.LastModified.Format(time.RFC3339),
		bucket.Status,
	}
	// Writing data row CSV
	if err := writer.Write(row); err != nil {
		log.Fatal("Could not write bucket data to CSV:", err)
	}
}

func CSVDBucketDelete(bucket *models.Bucket) {
	metaFilePath := "data/BucketMetaData.csv"

	metaFile, err := os.OpenFile(metaFilePath, os.O_RDWR, 0o644)
	if err != nil {
		log.Fatal("error: ", err)
	}
	defer metaFile.Close()

	// Read CSV
	reader := csv.NewReader(metaFile)
	records, err := reader.ReadAll()
	if err != nil {
		log.Fatal("Error reading CSV file: ", err)
	}

	bucketIndex := -1
	// searching index bucket to delete it
	for i, record := range records {
		if record[0] == bucket.Name {
			bucketIndex = i
			break
		}
	}

	// if bucket doesn't found outputing info
	if bucketIndex == -1 {
		log.Println("Bucket not found in CSV:", bucket.Name)
		return
	}

	records = append(records[:bucketIndex], records[bucketIndex+1:]...)

	// Clearing and opening file again
	metaFile, err = os.OpenFile(metaFilePath, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		log.Fatal("Error opening CSV file for writing: ", err)
	}
	defer metaFile.Close()

	// overwriting new list CSV
	writer := csv.NewWriter(metaFile)
	defer writer.Flush()

	if err := writer.WriteAll(records); err != nil {
		log.Fatal("Error writing updated CSV records: ", err)
	}
}

func CSVObjectWriter(object *models.Object, bucketName string) {
	dataDir := "data/"
	metaFilePath := dataDir + bucketName + "/ObjectMetaData.csv"

	metaFile, err := os.OpenFile(metaFilePath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		log.Fatal("error: ", err)
	}

	info, err := metaFile.Stat()
	if err != nil {
		log.Fatal("error: ", err)
	}
	// Adding header if its empty
	if info.Size() == 0 {
		_, err = metaFile.WriteString("ObjectKey,Size,ContentType,LastModifiedTime\n")
		if err != nil {
			log.Fatal("Can't create Header in CSV:", err)
		}
	}

	defer metaFile.Close()

	writer := csv.NewWriter(metaFile)
	defer writer.Flush()

	// Adding new row with info about object
	_, err = metaFile.Seek(0, io.SeekEnd)
	if err != nil {
		log.Fatal("error:", err)
	}
	row := []string{
		object.ObjectKey,
		strconv.Itoa(object.Size),
		object.ContentType,
		object.LastModified.Format(time.RFC3339),
	}
	if err := writer.Write(row); err != nil {
		log.Fatal("Could not write object data to CSV:", err)
	}
}

func CSVDeleteObject(object *models.Object, bucketName string) {
	metaFilePath := filepath.Join("data", bucketName, "ObjectMetaData.csv")

	metaFile, err := os.OpenFile(metaFilePath, os.O_RDWR, 0o644)
	if err != nil {
		log.Fatalf("Error opening CSV file: %v", err)
	}
	defer metaFile.Close()

	reader := csv.NewReader(metaFile)
	records, err := reader.ReadAll()
	if err != nil {
		log.Fatalf("Error reading CSV file: %v", err)
	}

	// searching key and indexing it
	objectIndex := -1
	for i, record := range records {
		if record[0] == object.ObjectKey {
			objectIndex = i
			log.Printf("Object '%s' found at index %d in CSV", object.ObjectKey, i)
			break
		}
	}

	// massaging if object doesn't found
	if objectIndex == -1 {
		log.Printf("Object not found in CSV: %s", object.ObjectKey)
		return
	}

	records = append(records[:objectIndex], records[objectIndex+1:]...)
	log.Printf("Object '%s' metadata removed from CSV", object.ObjectKey)

	// overwriting the file with the new data
	metaFile, err = os.OpenFile(metaFilePath, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		log.Fatalf("Error opening CSV file for writing: %v", err)
	}
	defer metaFile.Close()

	writer := csv.NewWriter(metaFile)
	defer writer.Flush()

	if err := writer.WriteAll(records); err != nil {
		log.Fatalf("Error writing updated CSV records: %v", err)
	}

	log.Printf("CSV updated successfully after deleting object '%s'", object.ObjectKey)
}

func CSVUpdateBucketMetaData(bucket *models.Bucket) {
	metaFilePath := "data/BucketMetaData.csv"

	metaFile, err := os.OpenFile(metaFilePath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		log.Fatal("Error opening CSV file: ", err)
	}
	defer metaFile.Close()

	reader := csv.NewReader(metaFile)
	records, err := reader.ReadAll()
	if err != nil {
		log.Fatal("Error reading CSV file: ", err)
	}

	// searching bucket by name
	bucketIndex := -1
	for i, record := range records {
		if record[0] == bucket.Name {
			bucketIndex = i
			break
		}
	}

	// massage if bucket doesn't found
	if bucketIndex == -1 {
		log.Printf("Bucket not found in CSV: %s", bucket.Name)
		return
	}

	// renewing bucket
	records[bucketIndex][1] = bucket.CreationTime.Format(time.RFC3339)
	records[bucketIndex][2] = bucket.LastModified.Format(time.RFC3339)
	records[bucketIndex][3] = bucket.Status

	// overwriting file with new name
	metaFile, err = os.OpenFile(metaFilePath, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		log.Fatal("Error opening CSV file for writing: ", err)
	}
	defer metaFile.Close()

	writer := csv.NewWriter(metaFile)
	defer writer.Flush()

	if err := writer.WriteAll(records); err != nil {
		log.Fatal("Error writing updated CSV records: ", err)
	}

	log.Printf("Bucket '%s' metadata updated successfully", bucket.Name)
}
