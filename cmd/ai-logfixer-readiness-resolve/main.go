package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CloudSpaceLab/ai-logfixer/internal/readinessresolve"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("ai-logfixer-readiness-resolve", flag.ContinueOnError)
	flags.SetOutput(stderr)

	inputPath := flags.String("input", "", "path to readiness candidate JSON input; defaults to AI_LOGFIXER_CANDIDATE_INPUT")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	path := strings.TrimSpace(*inputPath)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("AI_LOGFIXER_CANDIDATE_INPUT"))
	}
	if path == "" {
		_ = encodeResponse(stdout, readinessresolve.InvalidResponse("candidate input path is required"))
		fmt.Fprintln(stderr, "candidate input path is required; pass -input or set AI_LOGFIXER_CANDIDATE_INPUT")
		return 2
	}

	input, err := readinessresolve.LoadCandidateInput(path)
	if err != nil {
		_ = encodeResponse(stdout, readinessresolve.InvalidResponse(err.Error()))
		fmt.Fprintf(stderr, "load readiness candidate input: %v\n", err)
		return 2
	}

	response, err := readinessresolve.Resolve(context.Background(), input)
	if err != nil {
		if response.SchemaVersion == "" {
			response = readinessresolve.FailedResponse(input, err.Error())
		}
		if response.Message == "" {
			response.Message = err.Error()
		}
		_ = encodeResponse(stdout, response)
		fmt.Fprintf(stderr, "resolve readiness candidate: %v\n", err)
		return 1
	}
	if err := encodeResponse(stdout, response); err != nil {
		fmt.Fprintf(stderr, "encode readiness response: %v\n", err)
		return 1
	}
	return 0
}

func encodeResponse(writer io.Writer, response readinessresolve.Response) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
}
