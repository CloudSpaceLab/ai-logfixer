package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/frameworks/goravel"
	"github.com/CloudSpaceLab/ai-logfixer/internal/remediation"
)

type runOutput struct {
	InvestigationRequest contractsv1.InvestigationRequest `json:"investigation_request"`
	Diagnosis            contractsv1.DiagnosisResult      `json:"diagnosis"`
	RemediationPlan      contractsv1.RemediationPlan      `json:"remediation_plan"`
	Failure              failureOutput                    `json:"failure"`
	Route                routeOutput                      `json:"route"`
	Attempt              *contractsv1.RemediationAttempt  `json:"attempt,omitempty"`
	Receipt              *contractsv1.Receipt             `json:"receipt,omitempty"`
	SourceFile           *remediation.SourceFileResult    `json:"source_file,omitempty"`
}

type failureOutput struct {
	ServiceName string    `json:"service_name"`
	Method      string    `json:"method"`
	Route       string    `json:"route"`
	StatusClass int       `json:"status_class"`
	Count       int       `json:"count"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
}

type routeOutput struct {
	Method         string `json:"method"`
	Path           string `json:"path"`
	HandlerExpr    string `json:"handler_expr"`
	ControllerType string `json:"controller_type"`
	HandlerMethod  string `json:"handler_method"`
	SourceFile     string `json:"source_file"`
	HandlerFile    string `json:"handler_file"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("ai-logfixer-goravel", flag.ContinueOnError)
	flags.SetOutput(stderr)

	targetDir := flags.String("target", ".", "Goravel application target directory")
	serviceName := flags.String("service", "", "service name to record in contracts; defaults to target directory name")
	accessLogPath := flags.String("access-log", "", "Goravel access log path; defaults to the latest storage/logs/*.log")
	threshold := flags.Int("threshold", 3, "minimum repeated failure count required to start remediation planning")
	window := flags.Duration("window", 5*time.Minute, "time window for repeated failure detection")
	apply := flags.Bool("apply", false, "apply the narrow panic-line source patch after approval")
	approve := flags.Bool("approve-source-patch", false, "required with -apply; records operator approval for the source patch")
	backupDir := flags.String("backup-dir", "", "optional source backup directory")
	restartCommand := flags.String("restart-command", "", "optional command to restart or reload the Goravel service; runs from target directory")
	verifyCommand := flags.String("verify-command", "", "command to verify route recovery after patch; required with -apply")
	commandTimeout := flags.Duration("command-timeout", 30*time.Second, "timeout for restart and verify commands")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	targetAbs, err := filepath.Abs(filepath.Clean(*targetDir))
	if err != nil {
		fmt.Fprintf(stderr, "resolve target directory: %v\n", err)
		return 1
	}
	logPath, err := resolveAccessLogPath(targetAbs, *accessLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve Goravel access log: %v\n", err)
		return 1
	}

	now := time.Now().UTC()
	analysis, err := goravel.Analyze(goravel.Options{
		ServiceName:   *serviceName,
		TargetDir:     targetAbs,
		AccessLogPath: logPath,
		Threshold:     *threshold,
		Window:        *window,
		Now:           now,
	})
	if err != nil {
		fmt.Fprintf(stderr, "analyze Goravel app: %v\n", err)
		return 1
	}

	output := runOutput{
		InvestigationRequest: analysis.InvestigationRequest,
		Diagnosis:            analysis.Diagnosis,
		RemediationPlan:      analysis.RemediationPlan,
		Failure:              newFailureOutput(analysis.Failure),
		Route:                newRouteOutput(analysis.Route),
	}

	var runErr error
	if *apply {
		if !*approve {
			fmt.Fprintln(stderr, "-approve-source-patch is required with -apply")
			return 1
		}
		if strings.TrimSpace(*verifyCommand) == "" {
			fmt.Fprintln(stderr, "-verify-command is required with -apply")
			return 1
		}

		ctx := context.Background()
		var restart func(context.Context) error
		if strings.TrimSpace(*restartCommand) != "" {
			restart = func(ctx context.Context) error {
				return runShellCommand(ctx, *restartCommand, targetAbs, *commandTimeout)
			}
		}
		verify := func(ctx context.Context) error {
			return runShellCommand(ctx, *verifyCommand, targetAbs, *commandTimeout)
		}

		result, err := goravel.ExecutePanicPatch(ctx, analysis, goravel.ExecutionOptions{
			BackupDir: *backupDir,
			Now:       now,
			Restart:   restart,
			Verify:    verify,
		})
		if result.Attempt.ID != "" {
			output.Attempt = &result.Attempt
		}
		if result.Receipt.ID != "" {
			output.Receipt = &result.Receipt
		}
		if result.SourceFile.BackupPath != "" || result.SourceFile.Applied {
			output.SourceFile = &result.SourceFile
		}
		runErr = err
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintf(stderr, "encode result: %v\n", err)
		return 1
	}

	if runErr != nil {
		fmt.Fprintf(stderr, "Goravel source patch failed: %v\n", runErr)
		return 1
	}
	if *apply {
		fmt.Fprintln(stderr, "Goravel source patch completed")
	} else {
		fmt.Fprintln(stderr, "Goravel analysis dry run completed")
	}
	return 0
}

func resolveAccessLogPath(targetDir string, supplied string) (string, error) {
	if strings.TrimSpace(supplied) == "" {
		return latestAccessLog(targetDir)
	}
	if filepath.IsAbs(supplied) {
		return filepath.Clean(supplied), nil
	}
	return filepath.Join(targetDir, supplied), nil
}

func latestAccessLog(targetDir string) (string, error) {
	logDir := filepath.Join(targetDir, "storage", "logs")
	patterns := []string{"goravel*.log", "http*.log", "*.log"}
	seen := map[string]bool{}
	var candidates []logCandidate
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(logDir, pattern))
		if err != nil {
			return "", err
		}
		for _, match := range matches {
			if seen[match] {
				continue
			}
			seen[match] = true
			info, err := os.Stat(match)
			if err != nil || info.IsDir() {
				continue
			}
			candidates = append(candidates, logCandidate{path: match, modTime: info.ModTime()})
		}
	}
	if len(candidates) == 0 {
		return "", errors.New("no log files found under storage/logs; pass -access-log")
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	return candidates[0].path, nil
}

type logCandidate struct {
	path    string
	modTime time.Time
}

func newFailureOutput(failure goravel.FailureGroup) failureOutput {
	return failureOutput{
		ServiceName: failure.ServiceName,
		Method:      failure.Method,
		Route:       failure.Route,
		StatusClass: failure.StatusClass,
		Count:       failure.Count,
		Start:       failure.Start,
		End:         failure.End,
	}
}

func newRouteOutput(route goravel.RouteMapping) routeOutput {
	return routeOutput{
		Method:         route.Method,
		Path:           route.Path,
		HandlerExpr:    route.HandlerExpr,
		ControllerType: route.ControllerType,
		HandlerMethod:  route.HandlerMethod,
		SourceFile:     filepath.ToSlash(route.SourceFile),
		HandlerFile:    filepath.ToSlash(route.HandlerFile),
	}
}

func runShellCommand(ctx context.Context, commandLine string, workingDir string, timeout time.Duration) error {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" {
		return nil
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	name, args := shellCommand(commandLine)
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = workingDir
	devNull, openErr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if openErr != nil {
		return fmt.Errorf("open null device: %w", openErr)
	}
	defer devNull.Close()
	command.Stdout = devNull
	command.Stderr = devNull

	err := command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("command timed out after %s: %s", timeout, commandLine)
	}
	if err != nil {
		return fmt.Errorf("command failed: %s: %w", commandLine, err)
	}
	return nil
}

func shellCommand(commandLine string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", commandLine}
	}
	return "/bin/sh", []string{"-c", commandLine}
}
