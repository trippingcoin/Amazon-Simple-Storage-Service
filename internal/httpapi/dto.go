package httpapi

import (
	"A3S/internal/objectstore"
	"encoding/xml"
	"time"
)

type BucketResponse struct {
	XMLName      xml.Name  `xml:"Bucket"`
	Name         string    `xml:"Name"`
	CreationTime time.Time `xml:"CreationTime"`
	LastModified time.Time `xml:"LastModified"`
	Status       string    `xml:"Status"`
}

type ObjectResponse struct {
	XMLName      xml.Name  `xml:"Object"`
	ObjectKey    string    `xml:"ObjectKey"`
	Size         int64     `xml:"Size"`
	ContentType  string    `xml:"ContentType"`
	LastModified time.Time `xml:"LastModified"`
}

type ErrorResponse struct {
	XMLName xml.Name `xml:"Error"`
	Message string   `xml:"Message"`
	Code    int      `xml:"Code"`
}

// Legacy wire names stay here. The domain and filesystem never use the
// historical "data/" prefix as an object identifier.
func toObjectResponse(object objectstore.Object) ObjectResponse {
	return ObjectResponse{ObjectKey: "data/" + string(object.ID.Bucket) + "/" + string(object.ID.Key), Size: object.Size, ContentType: object.ContentType, LastModified: object.LastModified}
}
func toBucketResponses(buckets []objectstore.Bucket) []BucketResponse {
	out := make([]BucketResponse, 0, len(buckets))
	for _, b := range buckets {
		status := "Active"
		if b.Empty {
			status = "Marked for deletion"
		}
		out = append(out, BucketResponse{Name: string(b.ID), CreationTime: b.CreationTime, LastModified: b.LastModified, Status: status})
	}
	return out
}
