package main

import (
	"A3S/internal/utils"
	"fmt"
	"log"
	"net/http"
	"strconv"
)

func main() {
	utils.Checkflag()
	mux := http.NewServeMux()
	s := http.Server{
		Addr:    ":" + strconv.Itoa(*utils.Port),
		Handler: mux,
	}
	fmt.Printf("Server is running on port: %d\n", *utils.Port)

	if err := s.ListenAndServe(); err != nil {
		log.Fatalf("Error %v", err)
	}
}
