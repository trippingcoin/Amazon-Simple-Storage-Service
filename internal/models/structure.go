package models

import (
	"encoding/xml"
	"time"
)

type Bucket struct {
	XMLName      xml.Name  `xml:"Bucket"`
	Name         string    `xml:"Name"`
	CreationTime time.Time `xml:"CreationTime"`
	LastModified time.Time `xml:"LastModified"`
	Status       string    `xml:"Status"`
}

type Object struct {
	XMLName      xml.Name  `xml:"Object"`
	ObjectKey    string    `xml:"ObjectKey"`
	Size         int       `xml:"Size"`
	ContentType  string    `xml:"ContentType"`
	LastModified time.Time `xml:"LastModified"`
}

type Storage struct {
	Buckets []Bucket
	Object  []Object
}

type XMLErrorResponse struct {
	XmlName xml.Name `xml:"Error"`
	Message string   `xml:"Message"`
	Code    int      `xml:"Code"`
}
