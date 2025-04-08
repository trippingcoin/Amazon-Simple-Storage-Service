package main

import (
	bucketHandl "A3S/internal/handlers/bucketHandler"
	objectHandl "A3S/internal/handlers/objectHandler"
	rootHandl "A3S/internal/handlers/rootHandler"
	"A3S/internal/models"
	"A3S/internal/utils"
	"fmt"
	"log"
	"net/http"
	"strconv"
)

func main() {
	utils.Checkflag()

	system := &models.Storage{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandl.CreateRootHandler(system))
	mux.HandleFunc("/{bucket}", bucketHandl.CreateBucketHandler(system))
	mux.HandleFunc("/{bucket}/", bucketHandl.CreateBucketHandler(system))
	mux.HandleFunc("/{bucket}/{object}", objectHandl.CreateObjectHandler(system))
	mux.HandleFunc("/{bucket}/{object}/", objectHandl.CreateObjectHandler(system))

	s := http.Server{
		Addr:    ":" + strconv.Itoa(*utils.Port),
		Handler: mux,
	}
	fmt.Printf("Server is running on port: %d\n", *utils.Port)

	if err := s.ListenAndServe(); err != nil {
		log.Fatalf("Error %v", err)
	}
}
