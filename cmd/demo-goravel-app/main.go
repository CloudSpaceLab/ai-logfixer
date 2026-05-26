package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/CloudSpaceLab/ai-logfixer/internal/demoapp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "address for the demo app")
	configPath := flag.String("config", "./tmp/demo-goravel-app.json", "path to demo app config")
	logPath := flag.String("log", "./tmp/demo-goravel-app.log", "path to demo app log")
	initBroken := flag.Bool("init-broken", true, "write a broken upstream config before starting")
	flag.Parse()

	if *initBroken {
		if err := demoapp.WriteConfig(*configPath, demoapp.Config{
			ServiceName: "goravel-demo",
			UpstreamURL: "http://127.0.0.1:1/orders",
		}); err != nil {
			log.Fatalf("write broken config: %v", err)
		}
	}

	if err := os.MkdirAll("./tmp", 0o755); err != nil {
		log.Fatalf("create tmp directory: %v", err)
	}

	fmt.Printf("demo app listening on http://%s\n", *addr)
	fmt.Printf("config: %s\n", *configPath)
	fmt.Printf("log: %s\n", *logPath)
	log.Fatal(http.ListenAndServe(*addr, demoapp.NewHandler(*configPath, *logPath)))
}
