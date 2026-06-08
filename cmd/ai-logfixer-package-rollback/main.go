package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	packagerollback "github.com/CloudSpaceLab/ai-logfixer/internal/resolvers/packages"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("ai-logfixer-package-rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)

	packageFile := flags.String("package-file", "", "path to package.json")
	packageName := flags.String("package", "", "package name to roll back")
	currentSpec := flags.String("current", "", "current broken package spec from package.json")
	knownGoodSpec := flags.String("known-good", "", "known-good package spec to restore")
	verifyCommand := flags.String("verify-command", "", "command to verify the app after package rollback")
	verifyURL := flags.String("verify-url", "", "HTTP URL to verify the app after package rollback")
	expectedStatus := flags.Int("expected-status", 200, "expected HTTP status for -verify-url")
	workDir := flags.String("workdir", "", "working directory for -verify-command; defaults to package file directory")
	commandTimeout := flags.Duration("command-timeout", 30*time.Second, "timeout for verification command or HTTP request")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	result, err := packagerollback.Rollback(context.Background(), packagerollback.Options{
		PackageFile:    *packageFile,
		PackageName:    *packageName,
		CurrentSpec:    *currentSpec,
		KnownGoodSpec:  *knownGoodSpec,
		VerifyCommand:  *verifyCommand,
		VerifyURL:      *verifyURL,
		ExpectedStatus: *expectedStatus,
		WorkingDir:     *workDir,
		CommandTimeout: *commandTimeout,
	})

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(result); encodeErr != nil {
		fmt.Fprintf(stderr, "encode package rollback result: %v\n", encodeErr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "package rollback failed: %v\n", err)
		return 1
	}

	fmt.Fprintln(stderr, "Package rollback completed")
	return 0
}
