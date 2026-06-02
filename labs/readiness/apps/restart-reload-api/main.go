package main

import (
	"encoding/json"
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/orders/readiness", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := os.Stat("/tmp/serving-stale-config"); err == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "service still running with stale config; restart required",
				"lane":  "restart-reload",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "FIXED",
			"lane":   "restart-reload",
		})
	})

	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}
