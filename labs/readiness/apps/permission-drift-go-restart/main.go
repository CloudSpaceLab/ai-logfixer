package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func main() {
	startupErr := checkRuntimePermissions()

	http.HandleFunc("/orders/readiness", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/orders/readiness" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
			return
		}
		if startupErr != "" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": "permission drift: " + startupErr})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "FIXED", "lane": "permission-drift"})
	})

	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}

func checkRuntimePermissions() string {
	if _, err := os.ReadFile("data/readiness.json"); err != nil {
		return fmt.Sprintf("read data/readiness.json: %v", err)
	}
	file, err := os.OpenFile("data/startup.lock", os.O_WRONLY|os.O_APPEND, 0o664)
	if err != nil {
		return fmt.Sprintf("open data/startup.lock: %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString("startup readiness audit\n"); err != nil {
		return fmt.Sprintf("write data/startup.lock: %v", err)
	}
	return ""
}
