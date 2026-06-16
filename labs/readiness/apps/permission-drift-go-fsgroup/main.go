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

		if _, err := os.ReadFile("volume/status.json"); err != nil {
			log.Printf("permission drift: Kubernetes fsGroup-style volume read failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("permission drift: fsgroup read: %v", err)})
			return
		}

		file, err := os.OpenFile("volume/events.log", os.O_WRONLY|os.O_APPEND, 0o660)
		if err != nil {
			log.Printf("permission drift: Kubernetes fsGroup-style volume append failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("permission drift: fsgroup write: %v", err)})
			return
		}
		defer file.Close()
		if _, err := file.WriteString("readiness event\n"); err != nil {
			log.Printf("permission drift: Kubernetes fsGroup-style volume write failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("permission drift: fsgroup write: %v", err)})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "FIXED", "lane": "permission-drift"})
	})

	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}
