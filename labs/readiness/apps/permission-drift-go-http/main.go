package main

import (
	"encoding/json"
	"fmt"
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
		if err := os.WriteFile("data/audit.log", []byte("readiness audit\n"), 0o644); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("permission drift: %v", err)})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "FIXED", "lane": "permission-drift"})
	})

	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}
