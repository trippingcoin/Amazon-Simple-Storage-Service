package main

import (
	root "A3S/internal/handlers/rootHandler"
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
	mux.HandleFunc("/", root.CreateRootHandler(system))

	s := http.Server{
		Addr:    ":" + strconv.Itoa(*utils.Port),
		Handler: mux,
	}
	fmt.Printf("Server is running on port: %d\n", *utils.Port)

	if err := s.ListenAndServe(); err != nil {
		log.Fatalf("Error %v", err)
	}
}
