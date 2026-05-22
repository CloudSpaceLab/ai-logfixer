package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/CloudSpaceLab/ai-logfixer/internal/mvp"
)

func main() {
	serviceName := flag.String("service", "goravel-demo", "service name to investigate")
	baseURL := flag.String("base-url", "http://127.0.0.1:8090", "demo app base URL")
	configPath := flag.String("config", "./tmp/demo-goravel-app.json", "path to demo app config")
	logPath := flag.String("log", "./tmp/demo-goravel-app.log", "path to demo app log")
	healthyUpstream := flag.String("healthy-upstream", "http://127.0.0.1:8090/upstream/orders", "healthy upstream URL to patch into config")
	errorThreshold := flag.Int("threshold", 3, "minimum 503 count required to start remediation")
	flag.Parse()

	result, err := mvp.Run(context.Background(), mvp.Options{
		ServiceName:     *serviceName,
		BaseURL:         *baseURL,
		LogPath:         *logPath,
		ConfigPath:      *configPath,
		HealthyUpstream: *healthyUpstream,
		ErrorThreshold:  *errorThreshold,
	})
	if err != nil {
		log.Fatalf("run MVP: %v", err)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string]any{
		"investigation_request": result.InvestigationRequest,
		"diagnosis":             result.Diagnosis,
		"remediation_plan":      result.RemediationPlan,
		"attempt":               result.Attempt,
		"receipt":               result.Receipt,
		"backup_path":           result.BackupPath,
	}); err != nil {
		log.Fatalf("encode result: %v", err)
	}

	fmt.Fprintln(os.Stderr, "MVP remediation completed")
}
