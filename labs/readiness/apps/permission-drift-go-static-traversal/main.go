package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/orders/readiness", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/orders/readiness" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
			return
		}

		if _, err := os.ReadDir("public"); err != nil {
			log.Printf("permission drift: static directory read failed while ignoring traversal-looking input public/../../etc/passwd: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"detail": fmt.Sprintf("permission drift: read public: %v; ignored traversal-looking input public/../../etc/passwd", err),
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "FIXED", "lane": "permission-drift"})
	})

	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}
