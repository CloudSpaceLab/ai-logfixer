package laravel

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/agentfix"
	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

type Options struct {
	ServiceName    string
	TargetDir      string
	URL            string
	LogPath        string
	Apply          bool
	Now            time.Time
	HTTPHeaders    map[string]string
	HTTPStatusOnly bool

	ExternalAgent         bool
	ExternalAgentCommand  string
	ExternalAgentModel    string
	ExternalAgentName     string
	ExternalAgentValidate []string
	ExternalAgentKeepWork bool
	ExternalAgentMaxFiles int
	ExternalAgentRunner   agentfix.AgentRunner
}

type Result struct {
	InvestigationRequest contractsv1.InvestigationRequest `json:"investigation_request"`
	Diagnosis            contractsv1.DiagnosisResult      `json:"diagnosis"`
	RemediationPlan      contractsv1.RemediationPlan      `json:"remediation_plan"`
	Attempt              contractsv1.RemediationAttempt   `json:"attempt"`
	Receipt              contractsv1.Receipt              `json:"receipt"`
	BackupPath           string                           `json:"backup_path"`
	CreatedPath          string                           `json:"created_path"`
	HTTPProbe            HTTPProbe                        `json:"http_probe"`
	MissingClasses       []MissingClass                   `json:"missing_classes"`
	Issues               []LaravelIssue                   `json:"issues"`
	ExternalAgent        *agentfix.Result                 `json:"external_agent,omitempty"`
}

type HTTPProbe struct {
	URL              string `json:"url"`
	StatusCode       int    `json:"status_code"`
	LaravelErrorPage bool   `json:"laravel_error_page"`
	MatchedSignal    string `json:"matched_signal"`
	Excerpt          string `json:"excerpt"`
}

type LaravelIssue struct {
	Kind        string `json:"kind"`
	Message     string `json:"message"`
	Subject     string `json:"subject"`
	Source      string `json:"source"`
	File        string `json:"file"`
	Excerpt     string `json:"excerpt"`
	AutoFixable bool   `json:"auto_fixable"`
}

type MissingClass struct {
	Class           string           `json:"class"`
	Expected        string           `json:"expected"`
	References      []string         `json:"references"`
	FromLog         bool             `json:"from_log"`
	LogExcerpt      string           `json:"log_excerpt"`
	InferredMethods []InferredMethod `json:"inferred_methods"`
}

type InferredMethod struct {
	Name     string   `json:"name"`
	Static   bool     `json:"static"`
	Instance bool     `json:"instance"`
	Contexts []string `json:"contexts"`
}

func Run(ctx context.Context, options Options) (Result, error) {
	if options.TargetDir == "" {
		return Result{}, errors.New("target directory is required")
	}
	if options.ServiceName == "" {
		options.ServiceName = filepath.Base(filepath.Clean(options.TargetDir))
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}

	targetDir, err := filepath.Abs(filepath.Clean(options.TargetDir))
	if err != nil {
		return Result{}, fmt.Errorf("resolve target directory: %w", err)
	}
	options.TargetDir = targetDir

	if err := validateLaravelTarget(targetDir); err != nil {
		return Result{}, err
	}

	probe := HTTPProbe{}
	if options.URL != "" {
		probe = probeURL(ctx, options)
		if options.HTTPStatusOnly && probe.StatusCode != 0 && probe.StatusCode < 500 {
			return Result{}, fmt.Errorf("HTTP status is %d; status-only mode did not detect a server error", probe.StatusCode)
		}
	}
	issues := detectLaravelIssues(options, probe)

	logMissing, logExcerpt := parseMissingClassFromLogs(options)
	referencedMissing, err := scanMissingAppClasses(targetDir)
	if err != nil {
		return Result{}, fmt.Errorf("scan target for missing classes: %w", err)
	}
	missing := mergeMissingClasses(logMissing, logExcerpt, referencedMissing)
	if err := inferMissingClassUsages(targetDir, missing); err != nil {
		return Result{}, fmt.Errorf("infer missing class usage: %w", err)
	}
	issues = mergeLaravelIssues(issues, issuesFromMissingClasses(missing))

	if len(missing) == 0 {
		if len(issues) > 0 || probe.LaravelErrorPage {
			reason := "No safe automatic Laravel patch matched this failure."
			if result, handled, err := maybeRunExternalAgent(ctx, options, probe, issues, missing, reason); handled || err != nil {
				return result, err
			}
			return buildUnsupportedLaravelResult(options, probe, issues, missing, reason)
		}
		return Result{}, errors.New("no missing Laravel class references or recognizable Laravel issues found")
	}

	selected, ok := selectAutoStubbableClass(missing)
	if !ok {
		reason := "Missing classes were found, but none are safe to stub automatically: " + missingClassList(missing)
		if result, handled, err := maybeRunExternalAgent(ctx, options, probe, issues, missing, reason); handled || err != nil {
			return result, err
		}
		return buildUnsupportedLaravelResult(options, probe, issues, missing, reason)
	}

	investigationRequest := buildInvestigationRequest(options, probe, selected)
	diagnosis := buildDiagnosis(options, probe, selected)
	remediationPlan := buildRemediationPlan(options, selected)

	if err := investigationRequest.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate diagnosis: %w", err)
	}
	if err := remediationPlan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate remediation plan: %w", err)
	}

	result := Result{
		InvestigationRequest: investigationRequest,
		Diagnosis:            diagnosis,
		RemediationPlan:      remediationPlan,
		HTTPProbe:            probe,
		MissingClasses:       missing,
		Issues:               issues,
		CreatedPath:          selected.Expected,
	}

	if !options.Apply {
		attempt := buildSkippedAttempt(options, "Dry run completed; no files were changed.")
		receipt := buildDryRunReceipt(options, selected)
		result.Attempt = attempt
		result.Receipt = receipt
		return result, nil
	}

	backupPath, err := backupMissingFileMarker(selected.Expected, options.Now)
	if err != nil {
		return Result{}, fmt.Errorf("prepare rollback marker: %w", err)
	}
	result.BackupPath = backupPath

	stubSource, err := buildCompatibilityStub(selected)
	if err != nil {
		return Result{}, fmt.Errorf("build compatibility stub: %w", err)
	}
	if err := writeGeneratedStub(selected.Expected, stubSource); err != nil {
		return Result{}, fmt.Errorf("write generated compatibility stub: %w", err)
	}

	if err := maybeRunPHPLint(selected.Expected); err != nil {
		_ = os.Remove(selected.Expected)
		return Result{}, fmt.Errorf("verify PHP syntax: %w", err)
	}

	afterProbe := probe
	if options.URL != "" {
		afterProbe = probeURL(ctx, options)
		if afterProbe.StatusCode >= 500 || afterProbe.LaravelErrorPage {
			_ = os.Remove(selected.Expected)
			return Result{}, fmt.Errorf("verification failed after fallback service creation: status=%d laravel_error_page=%t signal=%s", afterProbe.StatusCode, afterProbe.LaravelErrorPage, afterProbe.MatchedSignal)
		}
	}
	result.HTTPProbe = afterProbe

	attempt := buildSucceededAttempt(options, afterProbe)
	receipt := buildReceipt(options, selected)
	if err := attempt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate remediation attempt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate receipt: %w", err)
	}
	result.Attempt = attempt
	result.Receipt = receipt
	return result, nil
}

func validateLaravelTarget(targetDir string) error {
	if _, err := os.Stat(filepath.Join(targetDir, "artisan")); err != nil {
		return fmt.Errorf("target does not look like a Laravel app: missing artisan: %w", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "composer.json")); err != nil {
		return fmt.Errorf("target does not look like a Laravel app: missing composer.json: %w", err)
	}
	return nil
}

func probeURL(ctx context.Context, options Options) HTTPProbe {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, options.URL, nil)
	if err != nil {
		return HTTPProbe{URL: options.URL, MatchedSignal: "request_build_failed: " + err.Error()}
	}
	for name, value := range options.HTTPHeaders {
		request.Header.Set(name, value)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return HTTPProbe{URL: options.URL, MatchedSignal: "request_failed: " + err.Error()}
	}
	defer response.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	body := string(raw)
	matched, signal := isLaravelProductionErrorPage(body)
	if response.StatusCode >= 500 {
		matched = true
		if signal == "" {
			signal = fmt.Sprintf("http_status=%d", response.StatusCode)
		}
	}

	return HTTPProbe{
		URL:              options.URL,
		StatusCode:       response.StatusCode,
		LaravelErrorPage: matched,
		MatchedSignal:    signal,
		Excerpt:          safeExcerpt(body),
	}
}

func isLaravelProductionErrorPage(body string) (bool, string) {
	normalized := strings.ToLower(strings.Join(strings.Fields(body), " "))
	signals := []string{
		"server unable to attend to this request at this time",
		"server cannot attend to this request at this time",
		"<title>sorry</title>",
		">sorry.<",
		"whoops, something went wrong",
	}
	for _, signal := range signals {
		if strings.Contains(normalized, signal) {
			return true, signal
		}
	}
	if strings.Contains(normalized, "go back") && strings.Contains(normalized, "sorry") && strings.Contains(normalized, "nunito") {
		return true, "laravel_illustrated_error_layout"
	}
	return false, ""
}

func parseMissingClassFromLogs(options Options) (MissingClass, string) {
	logPath, content := readLaravelLogTail(options)
	if logPath == "" {
		return MissingClass{}, ""
	}
	re := regexp.MustCompile(`Class "([^"]+)" not found(?: \(View: ([^)]+)\))?`)
	matches := re.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return MissingClass{}, ""
	}

	last := matches[len(matches)-1]
	class := strings.TrimSpace(last[1])
	expected := expectedClassPath(options.TargetDir, class)
	refs := []string{}
	if len(last) > 2 && last[2] != "" {
		refs = append(refs, filepath.Clean(last[2]))
	}

	return MissingClass{
		Class:      class,
		Expected:   expected,
		References: refs,
		FromLog:    true,
		LogExcerpt: safeExcerpt(content[strings.LastIndex(content, last[0]):]),
	}, logPath
}

func detectLaravelIssues(options Options, probe HTTPProbe) []LaravelIssue {
	logPath, content := readLaravelLogTail(options)
	var issues []LaravelIssue
	if content != "" {
		issues = append(issues, parseLaravelLogIssues(logPath, content)...)
	}
	if len(issues) == 0 && probe.LaravelErrorPage {
		issues = append(issues, LaravelIssue{
			Kind:    "unknown_laravel_error_page",
			Message: "Laravel rendered a production error page, but no supported log signature was found.",
			Subject: defaultString(probe.MatchedSignal,
				"laravel_error_page"),
			Source:  defaultString(probe.URL, "http_probe"),
			Excerpt: safeExcerpt(probe.Excerpt),
		})
	}
	return mergeLaravelIssues(issues)
}

func readLaravelLogTail(options Options) (string, string) {
	logPath := options.LogPath
	if logPath == "" {
		logPath = latestLaravelLog(filepath.Join(options.TargetDir, "storage", "logs"))
	}
	if logPath == "" {
		return "", ""
	}

	raw, err := os.ReadFile(filepath.Clean(logPath))
	if err != nil {
		return logPath, ""
	}
	return logPath, tailString(string(raw), 256*1024)
}

func parseLaravelLogIssues(source string, content string) []LaravelIssue {
	var issues []LaravelIssue
	appendLaravelLogIssue(&issues, source, content, "missing_class", "Missing PHP class", regexp.MustCompile(`Class "([^"]+)" not found(?: \(View: ([^)]+)\))?`), func(match []string) string {
		return match[1]
	})
	appendLaravelLogIssue(&issues, source, content, "undefined_method", "Undefined PHP method", regexp.MustCompile(`Call to undefined method ([A-Za-z_\\][A-Za-z0-9_\\]*)::([A-Za-z_][A-Za-z0-9_]*)\(`), func(match []string) string {
		return match[1] + "::" + match[2]
	})
	appendLaravelLogIssue(&issues, source, content, "view_not_found", "Missing Laravel view", regexp.MustCompile(`View \[([^\]]+)\] not found`), func(match []string) string {
		return match[1]
	})
	appendLaravelLogIssue(&issues, source, content, "missing_table", "Database table missing", regexp.MustCompile(`Base table or view not found: 1146 Table '([^']+)' doesn't exist`), func(match []string) string {
		return match[1]
	})
	appendLaravelLogIssue(&issues, source, content, "missing_column", "Database column missing", regexp.MustCompile(`Unknown column '([^']+)'`), func(match []string) string {
		return match[1]
	})
	appendLaravelLogIssue(&issues, source, content, "permission_denied", "Runtime permission denied", regexp.MustCompile(`(?i)(permission denied|failed to open stream: Permission denied)`), func(match []string) string {
		return match[1]
	})
	appendLaravelLogIssue(&issues, source, content, "syntax_error", "PHP syntax or parse error", regexp.MustCompile(`(?i)(parse error|syntax error|unexpected token[^\n]*)`), func(match []string) string {
		return match[1]
	})
	appendLaravelLogIssue(&issues, source, content, "undefined_symbol", "Undefined PHP symbol", regexp.MustCompile(`(?i)(undefined (?:variable|array key|property)[^\n]*)`), func(match []string) string {
		return match[1]
	})
	appendLaravelLogIssue(&issues, source, content, "route_not_defined", "Missing named route", regexp.MustCompile(`Route \[([^\]]+)\] not defined`), func(match []string) string {
		return match[1]
	})
	appendLaravelLogIssue(&issues, source, content, "binding_resolution", "Laravel container binding failed", regexp.MustCompile(`Target class \[([^\]]+)\] does not exist`), func(match []string) string {
		return match[1]
	})

	if len(issues) == 0 {
		appendLaravelLogIssue(&issues, source, content, "laravel_exception", "Laravel exception found in log", regexp.MustCompile(`production\.ERROR:\s+([^\n]+)`), func(match []string) string {
			return safeExcerpt(match[1])
		})
	}

	return mergeLaravelIssues(issues)
}

func appendLaravelLogIssue(issues *[]LaravelIssue, source string, content string, kind string, message string, pattern *regexp.Regexp, subject func([]string) string) {
	matches := pattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return
	}
	last := matches[len(matches)-1]
	matchedText := last[0]
	start := strings.LastIndex(content, matchedText)
	excerpt := matchedText
	if start >= 0 {
		excerpt = content[start:]
	}
	item := LaravelIssue{
		Kind:        kind,
		Message:     message,
		Subject:     strings.TrimSpace(subject(last)),
		Source:      source,
		Excerpt:     safeExcerpt(excerpt),
		AutoFixable: false,
	}
	*issues = append(*issues, item)
}

func latestLaravelLog(logDir string) string {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "laravel") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{
			path: filepath.Join(logDir, name),
			mod:  info.ModTime(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.After(candidates[j].mod) })
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].path
}

func scanMissingAppClasses(targetDir string) ([]MissingClass, error) {
	psr4 := appPSR4Root(targetDir)
	roots := []string{
		filepath.Join(targetDir, "app"),
		filepath.Join(targetDir, "resources", "views"),
		filepath.Join(targetDir, "routes"),
	}
	re := regexp.MustCompile(`\\?App\\(?:[A-Za-z_][A-Za-z0-9_]*\\)+[A-Za-z_][A-Za-z0-9_]*`)
	hits := map[string]map[string]struct{}{}

	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "vendor" || entry.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".php") && !strings.HasSuffix(name, ".blade.php") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content := string(raw)
			for _, indexes := range re.FindAllStringIndex(content, -1) {
				if !looksLikeClassUsage(content, indexes[0], indexes[1]) {
					continue
				}
				class := strings.TrimPrefix(content[indexes[0]:indexes[1]], `\`)
				if hits[class] == nil {
					hits[class] = map[string]struct{}{}
				}
				rel, _ := filepath.Rel(targetDir, path)
				hits[class][rel] = struct{}{}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	var missing []MissingClass
	for class, refs := range hits {
		expected := expectedClassPathForRoot(psr4, class)
		if expected == "" {
			continue
		}
		if _, err := os.Stat(expected); err == nil {
			continue
		}
		refList := make([]string, 0, len(refs))
		for ref := range refs {
			refList = append(refList, ref)
		}
		sort.Strings(refList)
		missing = append(missing, MissingClass{
			Class:      class,
			Expected:   expected,
			References: refList,
		})
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Class < missing[j].Class })
	return missing, nil
}

func appPSR4Root(targetDir string) string {
	raw, err := os.ReadFile(filepath.Join(targetDir, "composer.json"))
	if err != nil {
		return filepath.Join(targetDir, "app")
	}
	var composer struct {
		Autoload struct {
			PSR4 map[string]any `json:"psr-4"`
		} `json:"autoload"`
	}
	if err := json.Unmarshal(raw, &composer); err != nil {
		return filepath.Join(targetDir, "app")
	}
	value, ok := composer.Autoload.PSR4[`App\`]
	if !ok {
		return filepath.Join(targetDir, "app")
	}
	switch typed := value.(type) {
	case string:
		return filepath.Join(targetDir, filepath.FromSlash(typed))
	case []any:
		if len(typed) > 0 {
			if first, ok := typed[0].(string); ok {
				return filepath.Join(targetDir, filepath.FromSlash(first))
			}
		}
	}
	return filepath.Join(targetDir, "app")
}

func expectedClassPath(targetDir string, class string) string {
	return expectedClassPathForRoot(appPSR4Root(targetDir), class)
}

func expectedClassPathForRoot(appRoot string, class string) string {
	if !strings.HasPrefix(class, `App\`) {
		return ""
	}
	relative := strings.TrimPrefix(class, `App\`)
	return filepath.Join(appRoot, filepath.FromSlash(strings.ReplaceAll(relative, `\`, `/`)+".php"))
}

func mergeMissingClasses(logMissing MissingClass, logSource string, scanned []MissingClass) []MissingClass {
	byClass := map[string]MissingClass{}
	for _, item := range scanned {
		byClass[item.Class] = item
	}
	if logMissing.Class != "" {
		if existing, ok := byClass[logMissing.Class]; ok {
			existing.FromLog = true
			existing.LogExcerpt = logMissing.LogExcerpt
			if logSource != "" {
				existing.References = append(existing.References, logSource)
			}
			existing.References = append(existing.References, logMissing.References...)
			existing.References = uniqueStrings(existing.References)
			byClass[logMissing.Class] = existing
		} else {
			if logSource != "" {
				logMissing.References = append(logMissing.References, logSource)
			}
			logMissing.References = uniqueStrings(logMissing.References)
			byClass[logMissing.Class] = logMissing
		}
	}

	missing := make([]MissingClass, 0, len(byClass))
	for _, item := range byClass {
		item.References = uniqueStrings(item.References)
		missing = append(missing, item)
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].FromLog != missing[j].FromLog {
			return missing[i].FromLog
		}
		return missing[i].Class < missing[j].Class
	})
	return missing
}

func selectAutoStubbableClass(missing []MissingClass) (MissingClass, bool) {
	for _, item := range missing {
		if isSafeToStub(item) {
			return item, true
		}
	}
	return MissingClass{}, false
}

func isSafeToStub(missing MissingClass) bool {
	if missing.Class == "" || missing.Expected == "" {
		return false
	}
	if _, err := os.Stat(missing.Expected); err == nil {
		return false
	}
	if !strings.HasPrefix(missing.Class, `App\`) {
		return false
	}

	blockedPrefixes := []string{
		`App\Http\Controllers\`,
		`App\Models\`,
		`App\Providers\`,
		`App\Console\`,
		`App\Exceptions\`,
		`App\Jobs\`,
		`App\Mail\`,
		`App\Notifications\`,
	}
	for _, prefix := range blockedPrefixes {
		if strings.HasPrefix(missing.Class, prefix) {
			return false
		}
	}

	return missing.FromLog || len(missing.References) > 0
}

func issuesFromMissingClasses(missing []MissingClass) []LaravelIssue {
	issues := make([]LaravelIssue, 0, len(missing))
	for _, item := range missing {
		issues = append(issues, LaravelIssue{
			Kind:        "missing_class",
			Message:     "Missing App class file",
			Subject:     item.Class,
			Source:      "target_scan",
			File:        item.Expected,
			Excerpt:     safeExcerpt(strings.Join(item.References, "\n")),
			AutoFixable: isSafeToStub(item),
		})
	}
	return issues
}

func mergeLaravelIssues(groups ...[]LaravelIssue) []LaravelIssue {
	seen := map[string]int{}
	var merged []LaravelIssue
	for _, group := range groups {
		for _, issue := range group {
			issue.Kind = strings.TrimSpace(issue.Kind)
			issue.Subject = strings.TrimSpace(issue.Subject)
			issue.Source = strings.TrimSpace(issue.Source)
			if issue.Kind == "" {
				continue
			}
			key := issue.Kind + "\x00" + issue.Subject + "\x00" + issue.Source + "\x00" + issue.File
			if index, ok := seen[key]; ok {
				if issue.AutoFixable {
					merged[index].AutoFixable = true
				}
				if merged[index].Excerpt == "" {
					merged[index].Excerpt = issue.Excerpt
				}
				if merged[index].Message == "" {
					merged[index].Message = issue.Message
				}
				continue
			}
			seen[key] = len(merged)
			merged = append(merged, issue)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].AutoFixable != merged[j].AutoFixable {
			return merged[i].AutoFixable
		}
		if merged[i].Kind != merged[j].Kind {
			return merged[i].Kind < merged[j].Kind
		}
		return merged[i].Subject < merged[j].Subject
	})
	return merged
}

func backupMissingFileMarker(path string, now time.Time) (string, error) {
	backupDir := filepath.Join(filepath.Dir(path), ".ai-logfixer-backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(path))
	backupPath := filepath.Join(backupDir, "missing-"+now.Format("20060102T150405Z")+"-"+hex.EncodeToString(sum[:6])+".marker")
	return backupPath, os.WriteFile(backupPath, []byte("file did not exist before ai-logfixer remediation\n"+path+"\n"), 0o644)
}

func writeGeneratedStub(path string, source string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing file: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(source), 0o644)
}

func maybeRunPHPLint(path string) error {
	if _, err := exec.LookPath("php"); err != nil {
		return nil
	}
	cmd := exec.Command("php", "-l", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type sourceFile struct {
	path    string
	rel     string
	content string
}

func inferMissingClassUsages(targetDir string, missing []MissingClass) error {
	files, err := laravelSourceFiles(targetDir)
	if err != nil {
		return err
	}

	for index := range missing {
		methodsByName := map[string]*InferredMethod{}
		references := map[string]struct{}{}
		for _, ref := range missing[index].References {
			references[ref] = struct{}{}
		}

		for _, file := range files {
			if !contentMentionsClass(file.content, missing[index].Class) {
				continue
			}
			references[file.rel] = struct{}{}
			for _, method := range inferMethodsFromContent(missing[index].Class, file.content) {
				existing := methodsByName[method.Name]
				if existing == nil {
					methodCopy := method
					methodsByName[method.Name] = &methodCopy
					continue
				}
				existing.Static = existing.Static || method.Static
				existing.Instance = existing.Instance || method.Instance
				existing.Contexts = uniqueStrings(append(existing.Contexts, method.Contexts...))
			}
		}

		methods := make([]InferredMethod, 0, len(methodsByName))
		for _, method := range methodsByName {
			method.Contexts = uniqueStrings(method.Contexts)
			methods = append(methods, *method)
		}
		sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })

		missing[index].InferredMethods = methods
		missing[index].References = uniqueStrings(mapKeys(references))
	}
	return nil
}

func laravelSourceFiles(targetDir string) ([]sourceFile, error) {
	roots := []string{
		filepath.Join(targetDir, "app"),
		filepath.Join(targetDir, "resources", "views"),
		filepath.Join(targetDir, "routes"),
	}
	var files []sourceFile
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "vendor" || entry.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".php") && !strings.HasSuffix(name, ".blade.php") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(targetDir, path)
			files = append(files, sourceFile{
				path:    path,
				rel:     rel,
				content: string(raw),
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func contentMentionsClass(content string, class string) bool {
	if strings.Contains(content, class) || strings.Contains(content, `\`+class) {
		return true
	}
	for _, alias := range aliasesForClass(content, class) {
		if strings.Contains(content, alias+"::") || strings.Contains(content, "new "+alias) || strings.Contains(content, alias+"::class") {
			return true
		}
	}
	return false
}

func looksLikeClassUsage(content string, start int, end int) bool {
	after := strings.TrimLeft(content[end:], " \t\r\n")
	if strings.HasPrefix(after, "::") {
		return true
	}

	beforeStart := start - 24
	if beforeStart < 0 {
		beforeStart = 0
	}
	before := content[beforeStart:start]
	newRe := regexp.MustCompile(`(?i)new\s+\\?$`)
	if newRe.MatchString(before) {
		return true
	}

	containerRe := regexp.MustCompile(`(?i)(app|make|resolve)\s*\(\s*\\?$`)
	return containerRe.MatchString(before)
}

func inferMethodsFromContent(class string, content string) []InferredMethod {
	expressions := []string{regexp.QuoteMeta(class), `\\` + regexp.QuoteMeta(class)}
	for _, alias := range aliasesForClass(content, class) {
		expressions = append(expressions, regexp.QuoteMeta(alias))
	}
	expressions = uniqueStrings(expressions)

	methodsByName := map[string]*InferredMethod{}
	instanceVars := map[string]struct{}{}
	for _, expression := range expressions {
		staticRe := regexp.MustCompile(`(?:` + expression + `)::([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
		for _, match := range staticRe.FindAllStringSubmatchIndex(content, -1) {
			name := content[match[2]:match[3]]
			addInferredMethod(methodsByName, name, true, false, callContext(content, match[0]))
		}

		newRe := regexp.MustCompile(`new\s+(?:` + expression + `)\s*\(`)
		for _, match := range newRe.FindAllStringSubmatchIndex(content, -1) {
			addInferredMethod(methodsByName, "__construct", false, true, "constructor")
			lineStart := strings.LastIndex(content[:match[0]], "\n") + 1
			line := content[lineStart:match[0]]
			assignRe := regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)\s*=\s*$`)
			assign := assignRe.FindStringSubmatch(line)
			if len(assign) == 2 {
				instanceVars[assign[1]] = struct{}{}
			}
		}
	}

	for variable := range instanceVars {
		instanceRe := regexp.MustCompile(`\$` + regexp.QuoteMeta(variable) + `->([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
		for _, match := range instanceRe.FindAllStringSubmatchIndex(content, -1) {
			name := content[match[2]:match[3]]
			addInferredMethod(methodsByName, name, false, true, callContext(content, match[0]))
		}
	}

	methods := make([]InferredMethod, 0, len(methodsByName))
	for _, method := range methodsByName {
		method.Contexts = uniqueStrings(method.Contexts)
		methods = append(methods, *method)
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })
	return methods
}

func aliasesForClass(content string, class string) []string {
	base := classBaseName(class)
	aliases := []string{}
	useRe := regexp.MustCompile(`(?m)^\s*use\s+` + regexp.QuoteMeta(class) + `(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?\s*;`)
	for _, match := range useRe.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 && match[1] != "" {
			aliases = append(aliases, match[1])
		} else {
			aliases = append(aliases, base)
		}
	}
	return uniqueStrings(aliases)
}

func addInferredMethod(methods map[string]*InferredMethod, name string, static bool, instance bool, context string) {
	if name == "" {
		return
	}
	method := methods[name]
	if method == nil {
		method = &InferredMethod{Name: name}
		methods[name] = method
	}
	method.Static = method.Static || static
	method.Instance = method.Instance || instance
	method.Contexts = append(method.Contexts, context)
}

func callContext(content string, offset int) string {
	lineStart := strings.LastIndex(content[:offset], "\n") + 1
	lineEnd := strings.Index(content[offset:], "\n")
	if lineEnd == -1 {
		lineEnd = len(content)
	} else {
		lineEnd = offset + lineEnd
	}
	line := content[lineStart:lineEnd]
	beforeCall := line[:offset-lineStart]
	if strings.Contains(beforeCall, "{{") || strings.Contains(beforeCall, "{!!") || strings.Contains(beforeCall, "echo ") {
		return "rendered_output"
	}
	if strings.Contains(line, "return ") {
		return "return_value"
	}
	return "side_effect"
}

func buildCompatibilityStub(missing MissingClass) (string, error) {
	namespace, className, err := splitClassName(missing.Class)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	builder.WriteString("<?php\n\n")
	builder.WriteString("namespace " + namespace + ";\n\n")
	builder.WriteString("/**\n")
	builder.WriteString(" * Compatibility stub generated by ai-logfixer after a missing-class failure.\n")
	builder.WriteString(" * It is intentionally conservative: side-effect methods no-op, rendered\n")
	builder.WriteString(" * output is escaped, and unknown calls return safe empty values.\n")
	builder.WriteString(" */\n")
	builder.WriteString("class " + className + "\n{\n")

	for _, method := range missing.InferredMethods {
		if method.Name == "__construct" {
			builder.WriteString("    public function __construct(...$args)\n")
			builder.WriteString("    {\n")
			builder.WriteString("    }\n\n")
			continue
		}
		if method.Static && !method.Instance {
			builder.WriteString("    public static function " + method.Name + "(...$args)\n")
			builder.WriteString("    {\n")
			builder.WriteString("        return self::defaultReturn(__FUNCTION__, $args);\n")
			builder.WriteString("    }\n\n")
			continue
		}
		if method.Instance && !method.Static {
			builder.WriteString("    public function " + method.Name + "(...$args)\n")
			builder.WriteString("    {\n")
			builder.WriteString("        return self::defaultReturn(__FUNCTION__, $args);\n")
			builder.WriteString("    }\n\n")
		}
	}

	builder.WriteString(genericMagicFallbackSource)
	builder.WriteString("}\n")
	return builder.String(), nil
}

func splitClassName(class string) (string, string, error) {
	parts := strings.Split(class, `\`)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("cannot split class name %q", class)
	}
	return strings.Join(parts[:len(parts)-1], `\`), parts[len(parts)-1], nil
}

func classBaseName(class string) string {
	parts := strings.Split(class, `\`)
	return parts[len(parts)-1]
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func buildInvestigationRequest(options Options, probe HTTPProbe, missing MissingClass) contractsv1.InvestigationRequest {
	return contractsv1.InvestigationRequest{
		ID:              "inv_req_laravel_missing_class_001",
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "ai-logfixer-laravel",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         "Laravel production error page rendered",
		ErrorCode:       "laravel_error_page",
		TimeWindow: contractsv1.TimeWindow{
			Start: options.Now.Add(-10 * time.Minute),
			End:   options.Now,
		},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "laravel_missing_class",
			ErrorCode:     "class_not_found",
			Source:        "http_body_and_target_scan",
			DeployVersion: "target-directory",
			Tags:          []string{"laravel", "php", "production-error-page", "missing-class"},
		},
		DisplayStatus: "Laravel investigation started automatically",
		UserMessage:   fmt.Sprintf("I detected a Laravel production error signal and a missing class reference: %s.", missing.Class),
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildDiagnosis(options Options, probe HTTPProbe, missing MissingClass) contractsv1.DiagnosisResult {
	httpEvidence := contractsv1.EvidenceItem{
		ID:             "ev_laravel_http_error_page_001",
		Type:           contractsv1.EvidenceTypeTrace,
		Source:         defaultString(probe.URL, "target scan"),
		Timestamp:      options.Now,
		Title:          "Laravel production error page signal",
		Summary:        fmt.Sprintf("HTTP status was %d; Laravel error page signal was %t (%s).", probe.StatusCode, probe.LaravelErrorPage, probe.MatchedSignal),
		RawExcerpt:     safeExcerpt(probe.Excerpt),
		RedactionState: contractsv1.RedactionStateRedacted,
		RelatedIDs:     []string{"ev_laravel_missing_class_001"},
		UIHints: contractsv1.UIHints{
			Icon:     "file-warning",
			Tone:     "danger",
			Sections: []string{"http", "evidence"},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
	}
	missingEvidence := contractsv1.EvidenceItem{
		ID:             "ev_laravel_missing_class_001",
		Type:           contractsv1.EvidenceTypeTrace,
		Source:         options.TargetDir,
		Timestamp:      options.Now,
		Title:          "Missing PSR-4 class file",
		Summary:        fmt.Sprintf("%s is referenced but %s does not exist.", missing.Class, missing.Expected),
		RawExcerpt:     safeExcerpt(strings.Join(missing.References, "\n")),
		RedactionState: contractsv1.RedactionStateRedacted,
		RelatedIDs:     []string{"ev_laravel_http_error_page_001"},
		UIHints: contractsv1.UIHints{
			Icon:     "code",
			Tone:     "warning",
			Sections: []string{"filesystem", "source"},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
	}

	return contractsv1.DiagnosisResult{
		ID:                   "diag_laravel_missing_class_001",
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               contractsv1.DiagnosisStatusComplete,
		Summary:              fmt.Sprintf("Laravel is failing because %s is referenced by the app but its PSR-4 file is missing.", missing.Class),
		Confidence:           0.94,
		SuspectedRootCause:   fmt.Sprintf("Deployment drift: %s was not present in the target directory while Blade/controller code references it.", missing.Expected),
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        []contractsv1.EvidenceItem{httpEvidence, missingEvidence},
		Recommendations:      []contractsv1.RunbookRecommendation{compatibilityStubRecommendation(missing)},
		PatchPlan:            compatibilityStubPatchPlan(missing),
		RollbackPlan:         compatibilityStubRollbackPlan(),
		SafetyClassification: contractsv1.SafetyLowRisk,
		DisplayStatus:        "Missing Laravel class diagnosed",
		UserMessage:          fmt.Sprintf("The failing page can be restored by creating %s as a conservative compatibility stub inferred from usage.", missing.Expected),
		NextActions: []contractsv1.NextAction{
			{
				ID:          "next_apply_laravel_missing_class_stub",
				Label:       "Apply compatibility stub",
				ActionType:  "apply_remediation",
				Description: "Create the missing class stub, lint it, and verify the page no longer renders Laravel's error page.",
				Enabled:     true,
			},
		},
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        "tl_laravel_missing_class_diagnosed_001",
				Type:      "diagnosis.completed",
				Message:   "Laravel missing class diagnosis completed.",
				Severity:  "info",
				Timestamp: options.Now,
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func compatibilityStubRecommendation(missing MissingClass) contractsv1.RunbookRecommendation {
	return contractsv1.RunbookRecommendation{
		ID:                  "rec_create_missing_class_stub_001",
		Title:               "Create missing compatibility stub",
		Reason:              fmt.Sprintf("The app calls %s, but its PSR-4 file is missing. A generated stub can restore the failing code path without changing routes or database schema.", missing.Class),
		Confidence:          0.91,
		Steps:               []string{"Infer methods from PHP/Blade usage.", "Create a rollback marker.", "Write the generated class stub.", "Run php -l when available.", "Re-probe the page."},
		RequiredPermissions: []string{"filesystem:write", "http:verify"},
		EstimatedRisk:       contractsv1.SafetyLowRisk,
		RequiresApproval:    false,
	}
}

func compatibilityStubPatchPlan(missing MissingClass) *contractsv1.PatchPlan {
	return &contractsv1.PatchPlan{
		ID:         "patch_laravel_missing_class_stub_001",
		TargetType: contractsv1.PatchTargetFile,
		TargetRefs: []string{missing.Expected},
		DiffPreview: contractsv1.DiffPreview{
			Before: "missing file",
			After:  fmt.Sprintf("create %s with inferred methods: %s", missing.Class, inferredMethodList(missing.InferredMethods)),
		},
		RiskLevel:        contractsv1.SafetyLowRisk,
		RequiresApproval: false,
		BlockedReasons:   []string{},
	}
}

func compatibilityStubRollbackPlan() *contractsv1.RollbackPlan {
	return &contractsv1.RollbackPlan{
		ID:                   "rollback_laravel_missing_class_stub_001",
		RollbackType:         contractsv1.RollbackReversePatch,
		SnapshotRefs:         []string{"missing_file_marker"},
		RestoreSteps:         []string{"Delete the generated class stub.", "Clear compiled Laravel views if needed.", "Re-run verification."},
		Limitations:          []string{"Rollback restores the pre-existing missing-file state and may bring the Laravel error page back."},
		RiskLevel:            contractsv1.SafetyLowRisk,
		RequiresManualReview: false,
	}
}

func buildRemediationPlan(options Options, missing MissingClass) contractsv1.RemediationPlan {
	return contractsv1.RemediationPlan{
		ID:                "rem_plan_laravel_missing_class_stub_001",
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: "diag_laravel_missing_class_001",
		Summary:           fmt.Sprintf("Create a conservative compatibility stub for missing Laravel class %s.", missing.Class),
		FixPreview: contractsv1.DiffPreview{
			Before: "missing " + missing.Expected,
			After:  "created " + missing.Expected,
		},
		RollbackPlan:     *compatibilityStubRollbackPlan(),
		RiskLevel:        contractsv1.SafetyLowRisk,
		ApprovalRequired: false,
		Status:           contractsv1.RemediationStatusApproved,
		DisplayStatus:    "Safe Laravel stub fix approved automatically",
		UserMessage:      "This low-risk remediation only creates a missing PSR-4 class stub inferred from observed usage and verifies it before reporting success.",
		NextActions:      []contractsv1.NextAction{{ID: "next_execute_laravel_stub_fix", Label: "Execute fix", ActionType: "execute_remediation", Description: "Create the class stub and verify recovery.", Enabled: true}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: "tl_laravel_remediation_plan_001", Type: "remediation.plan_created", Message: "Laravel missing class remediation plan created.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:     []contractsv1.ExternalRef{},
		KnowledgeRefs:    []contractsv1.KnowledgeRef{},
		CreatedAt:        options.Now,
	}
}

func buildSkippedAttempt(options Options, message string) contractsv1.RemediationAttempt {
	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)
	return contractsv1.RemediationAttempt{
		ID:                  "rem_attempt_laravel_missing_class_stub_001",
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   "rem_plan_laravel_missing_class_stub_001",
		ApprovalRequestID:   "dry_run_no_approval_required",
		Status:              contractsv1.RemediationStatusSucceeded,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary:      contractsv1.MonitorSummary{Status: "dry_run", Message: message, Signals: []string{"apply=false"}, Duration: "0s"},
		DisplayStatus:       "Dry run complete",
		UserMessage:         message,
		TimelineEvents:      []contractsv1.TimelineEvent{{ID: "tl_laravel_dry_run_001", Type: "remediation.dry_run", Message: message, Severity: "info", Timestamp: finished}},
		ExternalRefs:        []contractsv1.ExternalRef{},
	}
}

func buildSucceededAttempt(options Options, probe HTTPProbe) contractsv1.RemediationAttempt {
	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)
	return contractsv1.RemediationAttempt{
		ID:                  "rem_attempt_laravel_missing_class_stub_001",
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   "rem_plan_laravel_missing_class_stub_001",
		ApprovalRequestID:   "auto_approved_low_risk_laravel",
		Status:              contractsv1.RemediationStatusSucceeded,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary: contractsv1.MonitorSummary{
			Status:   "healthy",
			Message:  "Missing class stub was created and verification passed.",
			Signals:  []string{fmt.Sprintf("http_status=%d", probe.StatusCode), fmt.Sprintf("laravel_error_page=%t", probe.LaravelErrorPage)},
			Duration: "1s",
		},
		DisplayStatus:  "Fix applied and verified",
		UserMessage:    "I created the missing Laravel class stub and verified that the Laravel error page is no longer detected.",
		TimelineEvents: []contractsv1.TimelineEvent{{ID: "tl_laravel_remediation_succeeded_001", Type: "remediation.succeeded", Message: "Missing Laravel class stub created.", Severity: "info", Timestamp: finished}},
		ExternalRefs:   []contractsv1.ExternalRef{},
	}
}

func buildDryRunReceipt(options Options, missing MissingClass) contractsv1.Receipt {
	return contractsv1.Receipt{
		ID:                   "receipt_laravel_missing_class_stub_dry_run_001",
		DiagnosisID:          "diag_laravel_missing_class_001",
		RemediationPlanID:    "rem_plan_laravel_missing_class_stub_001",
		RemediationAttemptID: "rem_attempt_laravel_missing_class_stub_001",
		ActionTaken:          "dry run only",
		Actor:                "ai-logfixer-laravel",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          "missing " + missing.Expected,
		AfterState:           "unchanged",
		Outcome:              "dry_run",
		Summary:              "AI LogFixer diagnosed the missing Laravel class but did not apply changes.",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: "tl_laravel_dry_run_receipt_001", Type: "receipt.created", Message: "Dry-run receipt recorded.", Severity: "info", Timestamp: options.Now.Add(3 * time.Second)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
}

func buildReceipt(options Options, missing MissingClass) contractsv1.Receipt {
	return contractsv1.Receipt{
		ID:                   "receipt_laravel_missing_class_stub_001",
		DiagnosisID:          "diag_laravel_missing_class_001",
		RemediationPlanID:    "rem_plan_laravel_missing_class_stub_001",
		RemediationAttemptID: "rem_attempt_laravel_missing_class_stub_001",
		ActionTaken:          "created missing Laravel class stub",
		Actor:                "ai-logfixer-laravel",
		Approver:             "auto_approved_low_risk_laravel",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          "missing " + missing.Expected,
		AfterState:           "created " + missing.Expected,
		Outcome:              "succeeded",
		Summary:              "AI LogFixer detected a Laravel missing class failure, created an inferred compatibility stub, and verified recovery.",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: "tl_laravel_receipt_created_001", Type: "receipt.created", Message: "Receipt recorded for Laravel missing service remediation.", Severity: "info", Timestamp: options.Now.Add(3 * time.Second)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
}

func buildUnsupportedLaravelResult(options Options, probe HTTPProbe, issues []LaravelIssue, missing []MissingClass, reason string) (Result, error) {
	issues = mergeLaravelIssues(issues, issuesFromMissingClasses(missing))
	if len(issues) == 0 {
		issues = []LaravelIssue{{
			Kind:    "unknown_laravel_error",
			Message: "Laravel failure signal detected, but no specific signature matched.",
			Subject: defaultString(probe.MatchedSignal,
				"unknown"),
			Source:  defaultString(probe.URL, options.TargetDir),
			Excerpt: safeExcerpt(probe.Excerpt),
		}}
	}

	primary := primaryLaravelIssue(issues)
	investigationRequest := buildGenericInvestigationRequest(options, probe, primary)
	diagnosis := buildGenericDiagnosis(options, probe, primary, issues, reason)
	remediationPlan := buildManualReviewRemediationPlan(options, diagnosis.ID, primary, reason)
	attempt := buildEscalatedAttempt(options, remediationPlan.ID, reason)
	receipt := buildEscalatedReceipt(options, diagnosis.ID, remediationPlan.ID, attempt.ID, primary, reason)

	if err := investigationRequest.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate generic investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate generic diagnosis: %w", err)
	}
	if err := remediationPlan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate generic remediation plan: %w", err)
	}
	if err := attempt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate generic remediation attempt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate generic receipt: %w", err)
	}

	return Result{
		InvestigationRequest: investigationRequest,
		Diagnosis:            diagnosis,
		RemediationPlan:      remediationPlan,
		Attempt:              attempt,
		Receipt:              receipt,
		HTTPProbe:            probe,
		MissingClasses:       missing,
		Issues:               issues,
	}, nil
}

func maybeRunExternalAgent(ctx context.Context, options Options, probe HTTPProbe, issues []LaravelIssue, missing []MissingClass, reason string) (Result, bool, error) {
	if !options.ExternalAgent {
		return Result{}, false, nil
	}

	issues = mergeLaravelIssues(issues, issuesFromMissingClasses(missing))
	agentResult, err := agentfix.Run(ctx, agentfix.Options{
		TargetDir:          options.TargetDir,
		Prompt:             buildLaravelExternalAgentPrompt(options, probe, issues, missing, reason),
		AgentCommand:       options.ExternalAgentCommand,
		AgentModel:         options.ExternalAgentModel,
		AgentName:          options.ExternalAgentName,
		ValidationCommands: options.ExternalAgentValidate,
		ExcludePaths:       []string{"storage/logs", "storage/framework/cache", "bootstrap/cache"},
		Apply:              options.Apply,
		Now:                options.Now,
		KeepWorkdir:        options.ExternalAgentKeepWork,
		MaxChangedFiles:    options.ExternalAgentMaxFiles,
		AutoPHPLint:        true,
		AgentRunner:        options.ExternalAgentRunner,
	})
	if err != nil {
		result, buildErr := buildUnsupportedLaravelResult(options, probe, issues, missing, reason+" External agent failed before producing an applicable patch: "+err.Error())
		result.ExternalAgent = &agentResult
		return result, true, buildErr
	}
	if len(agentResult.Changes) == 0 {
		result, buildErr := buildUnsupportedLaravelResult(options, probe, issues, missing, reason+" External agent completed without proposing file changes.")
		result.ExternalAgent = &agentResult
		return result, true, buildErr
	}
	if !agentResult.ValidationPassed {
		result, buildErr := buildUnsupportedLaravelResult(options, probe, issues, missing, reason+" External agent proposed changes, but validation failed.")
		result.ExternalAgent = &agentResult
		return result, true, buildErr
	}
	if !options.Apply {
		result, buildErr := buildExternalAgentLaravelResult(options, probe, issues, missing, agentResult, false)
		return result, true, buildErr
	}
	if !agentResult.Applied {
		result, buildErr := buildUnsupportedLaravelResult(options, probe, issues, missing, reason+" External agent changes passed validation but were not applied.")
		result.ExternalAgent = &agentResult
		return result, true, buildErr
	}

	afterProbe := probe
	if options.URL != "" {
		afterProbe = probeURL(ctx, options)
		if afterProbe.StatusCode >= 500 || afterProbe.LaravelErrorPage {
			rollbackMessage := "rollback was not available"
			if agentResult.ManifestPath != "" {
				if rollbackErr := agentfix.Rollback(agentResult.ManifestPath); rollbackErr != nil {
					rollbackMessage = "rollback failed: " + rollbackErr.Error()
				} else {
					rollbackMessage = "rollback completed"
				}
			}
			result, buildErr := buildUnsupportedLaravelResult(options, afterProbe, issues, missing, reason+" External agent patch was applied, but URL verification still failed; "+rollbackMessage+".")
			result.ExternalAgent = &agentResult
			return result, true, buildErr
		}
	}

	result, buildErr := buildExternalAgentLaravelResult(options, afterProbe, issues, missing, agentResult, true)
	return result, true, buildErr
}

func buildLaravelExternalAgentPrompt(options Options, probe HTTPProbe, issues []LaravelIssue, missing []MissingClass, reason string) string {
	payload := struct {
		Service        string         `json:"service"`
		TargetDir      string         `json:"target_dir"`
		URL            string         `json:"url"`
		Reason         string         `json:"reason"`
		HTTPProbe      HTTPProbe      `json:"http_probe"`
		Issues         []LaravelIssue `json:"issues"`
		MissingClasses []MissingClass `json:"missing_classes"`
	}{
		Service:        options.ServiceName,
		TargetDir:      options.TargetDir,
		URL:            options.URL,
		Reason:         reason,
		HTTPProbe:      probe,
		Issues:         issues,
		MissingClasses: missing,
	}
	raw, _ := json.MarshalIndent(payload, "", "  ")
	return `Fix this Laravel production failure in the staging copy.

The target may be returning Laravel's friendly "Sorry" page with HTTP 200, so do not trust status codes alone. Use the evidence below, inspect the codebase, and make the smallest source-level fix that is likely to resolve the page. If this is a database schema issue, prefer a Laravel migration or model/query fix instead of assuming direct database access. If the evidence is insufficient, leave the tree unchanged.

Evidence JSON:
` + string(raw)
}

func buildExternalAgentLaravelResult(options Options, probe HTTPProbe, issues []LaravelIssue, missing []MissingClass, agentResult agentfix.Result, applied bool) (Result, error) {
	issues = mergeLaravelIssues(issues, issuesFromMissingClasses(missing))
	if len(issues) == 0 {
		issues = []LaravelIssue{{
			Kind:    "external_agent_laravel_issue",
			Message: "Laravel issue handled by external agent",
			Subject: defaultString(probe.MatchedSignal,
				"external_agent"),
			Source: defaultString(probe.URL, options.TargetDir),
		}}
	}
	primary := primaryLaravelIssue(issues)
	investigationRequest := buildGenericInvestigationRequest(options, probe, primary)
	diagnosis := buildExternalAgentDiagnosis(options, probe, primary, issues, agentResult, applied)
	remediationPlan := buildExternalAgentRemediationPlan(options, diagnosis.ID, primary, agentResult, applied)
	attempt := buildExternalAgentAttempt(options, remediationPlan.ID, agentResult, applied)
	receipt := buildExternalAgentReceipt(options, diagnosis.ID, remediationPlan.ID, attempt.ID, primary, agentResult, applied)

	if err := investigationRequest.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate external-agent investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate external-agent diagnosis: %w", err)
	}
	if err := remediationPlan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate external-agent remediation plan: %w", err)
	}
	if err := attempt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate external-agent attempt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate external-agent receipt: %w", err)
	}

	return Result{
		InvestigationRequest: investigationRequest,
		Diagnosis:            diagnosis,
		RemediationPlan:      remediationPlan,
		Attempt:              attempt,
		Receipt:              receipt,
		HTTPProbe:            probe,
		MissingClasses:       missing,
		Issues:               issues,
		ExternalAgent:        &agentResult,
	}, nil
}

func buildExternalAgentDiagnosis(options Options, probe HTTPProbe, primary LaravelIssue, issues []LaravelIssue, agentResult agentfix.Result, applied bool) contractsv1.DiagnosisResult {
	evidence := genericLaravelEvidenceItems(options, probe, issues)
	evidence = append(evidence, contractsv1.EvidenceItem{
		ID:             "ev_laravel_external_agent_001",
		Type:           contractsv1.EvidenceTypeTrace,
		Source:         defaultString(agentResult.StagingDir, options.TargetDir),
		Timestamp:      options.Now,
		Title:          "External agent patch proposal",
		Summary:        fmt.Sprintf("External agent produced %d changed file(s): %s.", len(agentResult.Changes), agentChangeList(agentResult.Changes)),
		RawExcerpt:     safeExcerpt(agentResult.AgentOutput.Stdout + "\n" + agentResult.AgentOutput.Stderr),
		RedactionState: contractsv1.RedactionStateRedacted,
		RelatedIDs:     []string{},
		UIHints: contractsv1.UIHints{
			Icon:     "bot",
			Tone:     "warning",
			Sections: []string{"external_agent", "patch"},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
	})

	statusText := "proposed"
	if applied {
		statusText = "applied and verified"
	}
	return contractsv1.DiagnosisResult{
		ID:                 "diag_laravel_external_agent_001",
		ContractVersion:    contractsv1.ContractVersion,
		SchemaURL:          contractsv1.DiagnosisSchemaURL,
		Status:             contractsv1.DiagnosisStatusComplete,
		Summary:            "Laravel failure was handled by an external coding agent: " + laravelIssueSummary(primary) + ".",
		Confidence:         0.72,
		SuspectedRootCause: "The built-in low-risk fixer did not match this failure, so ai-logfixer delegated to an external coding agent and validated the resulting patch.",
		AffectedServices:   []string{options.ServiceName},
		EvidenceItems:      evidence,
		Recommendations: []contractsv1.RunbookRecommendation{
			{
				ID:                  "rec_laravel_external_agent_patch_001",
				Title:               "Use validated external-agent patch",
				Reason:              "The external agent generated a focused source patch in staging and validation passed before target application.",
				Confidence:          0.72,
				Steps:               []string{"Review changed files.", "Validate the patch in staging.", "Apply the patch to target.", "Verify the failing URL.", "Use the rollback manifest if verification fails."},
				RequiredPermissions: []string{"filesystem:write", "agent:execute", "validation:execute", "http:verify"},
				EstimatedRisk:       contractsv1.SafetyMediumRisk,
				RequiresApproval:    false,
			},
		},
		PatchPlan: &contractsv1.PatchPlan{
			ID:         "patch_laravel_external_agent_001",
			TargetType: contractsv1.PatchTargetFile,
			TargetRefs: agentChangePaths(agentResult.Changes),
			DiffPreview: contractsv1.DiffPreview{
				Before: laravelIssueSummary(primary),
				After:  statusText + " external-agent changes: " + agentChangeList(agentResult.Changes),
			},
			RiskLevel:        contractsv1.SafetyMediumRisk,
			RequiresApproval: false,
			BlockedReasons:   []string{},
		},
		RollbackPlan:         externalAgentRollbackPlan(agentResult),
		SafetyClassification: contractsv1.SafetyMediumRisk,
		DisplayStatus:        "External-agent Laravel patch " + statusText,
		UserMessage:          "The built-in fixer delegated to an external coding agent, validated the patch, and recorded rollback metadata.",
		NextActions:          []contractsv1.NextAction{{ID: "next_review_external_agent_patch", Label: "Review external patch", ActionType: "review_patch", Description: "Inspect the changed files and rollback manifest.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: "tl_laravel_external_agent_diag_001", Type: "diagnosis.completed", Message: "External-agent Laravel diagnosis completed.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
}

func buildExternalAgentRemediationPlan(options Options, diagnosisID string, issue LaravelIssue, agentResult agentfix.Result, applied bool) contractsv1.RemediationPlan {
	status := contractsv1.RemediationStatusApproved
	display := "External-agent patch approved"
	if !applied {
		status = contractsv1.RemediationStatusAwaitingApproval
		display = "External-agent patch proposed"
	}
	return contractsv1.RemediationPlan{
		ID:                "rem_plan_laravel_external_agent_001",
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           "Apply validated external-agent patch for " + laravelIssueSummary(issue) + ".",
		FixPreview: contractsv1.DiffPreview{
			Before: laravelIssueSummary(issue),
			After:  agentChangeList(agentResult.Changes),
		},
		RollbackPlan:     *externalAgentRollbackPlan(agentResult),
		RiskLevel:        contractsv1.SafetyMediumRisk,
		ApprovalRequired: false,
		Status:           status,
		DisplayStatus:    display,
		UserMessage:      "External-agent changes are guarded by staging validation and rollback metadata.",
		NextActions:      []contractsv1.NextAction{{ID: "next_execute_external_agent_patch", Label: "Execute external patch", ActionType: "execute_remediation", Description: "Apply the validated external-agent patch and verify recovery.", Enabled: !applied}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: "tl_laravel_external_agent_plan_001", Type: "remediation.plan_created", Message: "External-agent remediation plan created.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:     []contractsv1.ExternalRef{},
		KnowledgeRefs:    []contractsv1.KnowledgeRef{},
		CreatedAt:        options.Now,
	}
}

func buildExternalAgentAttempt(options Options, planID string, agentResult agentfix.Result, applied bool) contractsv1.RemediationAttempt {
	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)
	status := contractsv1.RemediationStatusSucceeded
	display := "External-agent patch applied and verified"
	message := "External-agent patch was applied after staging validation and the failing URL no longer matched Laravel's error page."
	monitorStatus := "healthy"
	if !applied {
		display = "External-agent dry run complete"
		message = "External agent proposed a patch in staging, but apply=false left the target unchanged."
		monitorStatus = "dry_run"
	}
	return contractsv1.RemediationAttempt{
		ID:                  "rem_attempt_laravel_external_agent_001",
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "external_agent_enabled",
		Status:              status,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary:      contractsv1.MonitorSummary{Status: monitorStatus, Message: message, Signals: externalAgentSignals(agentResult), Duration: "1s"},
		DisplayStatus:       display,
		UserMessage:         message,
		TimelineEvents:      []contractsv1.TimelineEvent{{ID: "tl_laravel_external_agent_attempt_001", Type: "remediation.succeeded", Message: display, Severity: "info", Timestamp: finished}},
		ExternalRefs:        []contractsv1.ExternalRef{},
	}
}

func buildExternalAgentReceipt(options Options, diagnosisID string, planID string, attemptID string, issue LaravelIssue, agentResult agentfix.Result, applied bool) contractsv1.Receipt {
	outcome := "succeeded"
	action := "applied validated external-agent Laravel patch"
	after := "changed files: " + agentChangeList(agentResult.Changes)
	if !applied {
		outcome = "dry_run"
		action = "generated external-agent Laravel patch in staging"
		after = "target unchanged; proposed changes: " + agentChangeList(agentResult.Changes)
	}
	return contractsv1.Receipt{
		ID:                   "receipt_laravel_external_agent_001",
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attemptID,
		ActionTaken:          action,
		Actor:                "ai-logfixer-laravel",
		Approver:             "external_agent_enabled",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          laravelIssueSummary(issue),
		AfterState:           after,
		Outcome:              outcome,
		Summary:              "AI LogFixer delegated to an external coding agent, validated the proposed changes, and recorded rollback metadata.",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: "tl_laravel_external_agent_receipt_001", Type: "receipt.created", Message: "Receipt recorded for external-agent Laravel remediation.", Severity: "info", Timestamp: options.Now.Add(3 * time.Second)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
}

func externalAgentRollbackPlan(agentResult agentfix.Result) *contractsv1.RollbackPlan {
	return &contractsv1.RollbackPlan{
		ID:                   "rollback_laravel_external_agent_001",
		RollbackType:         contractsv1.RollbackSnapshot,
		SnapshotRefs:         []string{defaultString(agentResult.ManifestPath, "staging_only")},
		RestoreSteps:         []string{"Run ai-logfixer rollback with the recorded manifest.", "Re-run the failing URL verification."},
		Limitations:          []string{"Rollback uses file snapshots captured before applying the external-agent patch."},
		RiskLevel:            contractsv1.SafetyMediumRisk,
		RequiresManualReview: false,
	}
}

func agentChangePaths(changes []agentfix.Change) []string {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	if len(paths) == 0 {
		return []string{"no_file_changes"}
	}
	return paths
}

func agentChangeList(changes []agentfix.Change) string {
	if len(changes) == 0 {
		return "no file changes"
	}
	items := make([]string, 0, len(changes))
	for _, change := range changes {
		items = append(items, change.Type+":"+change.Path)
	}
	sort.Strings(items)
	return strings.Join(items, ", ")
}

func externalAgentSignals(agentResult agentfix.Result) []string {
	signals := []string{
		fmt.Sprintf("changed_files=%d", len(agentResult.Changes)),
		fmt.Sprintf("validation_passed=%t", agentResult.ValidationPassed),
		fmt.Sprintf("applied=%t", agentResult.Applied),
	}
	if agentResult.ManifestPath != "" {
		signals = append(signals, "rollback_manifest="+agentResult.ManifestPath)
	}
	return signals
}

func primaryLaravelIssue(issues []LaravelIssue) LaravelIssue {
	for _, issue := range issues {
		if issue.Kind != "unknown_laravel_error_page" && issue.Kind != "unknown_laravel_error" {
			return issue
		}
	}
	return issues[0]
}

func buildGenericInvestigationRequest(options Options, probe HTTPProbe, issue LaravelIssue) contractsv1.InvestigationRequest {
	return contractsv1.InvestigationRequest{
		ID:              "inv_req_laravel_issue_001",
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "ai-logfixer-laravel",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         "Laravel production failure detected",
		ErrorCode:       issue.Kind,
		TimeWindow: contractsv1.TimeWindow{
			Start: options.Now.Add(-10 * time.Minute),
			End:   options.Now,
		},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "laravel_" + issue.Kind,
			ErrorCode:     issue.Kind,
			Source:        defaultString(issue.Source, defaultString(probe.URL, "target-directory")),
			DeployVersion: "target-directory",
			Tags:          []string{"laravel", "php", "production-error-page", issue.Kind},
		},
		DisplayStatus: "Laravel issue classified",
		UserMessage:   "I classified the Laravel failure as: " + laravelIssueSummary(issue) + ".",
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildGenericDiagnosis(options Options, probe HTTPProbe, primary LaravelIssue, issues []LaravelIssue, reason string) contractsv1.DiagnosisResult {
	evidence := genericLaravelEvidenceItems(options, probe, issues)
	return contractsv1.DiagnosisResult{
		ID:                   "diag_laravel_issue_001",
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               contractsv1.DiagnosisStatusComplete,
		Summary:              "Laravel failure classified as " + laravelIssueSummary(primary) + ".",
		Confidence:           0.78,
		SuspectedRootCause:   "The target produced a Laravel failure signal, but the safe automatic remediation library does not have enough evidence to apply a low-risk patch.",
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        evidence,
		Recommendations:      []contractsv1.RunbookRecommendation{manualLaravelReviewRecommendation(primary, reason)},
		PatchPlan:            nil,
		RollbackPlan:         nil,
		SafetyClassification: contractsv1.SafetyBlocked,
		DisplayStatus:        "Laravel issue diagnosed; automatic fix blocked",
		UserMessage:          reason + " I recorded the issue classification and left the target unchanged.",
		NextActions: []contractsv1.NextAction{
			{
				ID:          "next_manual_laravel_review",
				Label:       "Review Laravel issue",
				ActionType:  "manual_review",
				Description: "Inspect the classified issue, recent deploy diff, and Laravel log excerpt before applying a patch.",
				Enabled:     true,
			},
		},
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        "tl_laravel_generic_diagnosis_001",
				Type:      "diagnosis.completed",
				Message:   "Laravel issue classification completed without an automatic patch.",
				Severity:  "warning",
				Timestamp: options.Now,
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func genericLaravelEvidenceItems(options Options, probe HTTPProbe, issues []LaravelIssue) []contractsv1.EvidenceItem {
	items := make([]contractsv1.EvidenceItem, 0, len(issues)+1)
	if probe.URL != "" {
		items = append(items, contractsv1.EvidenceItem{
			ID:             "ev_laravel_generic_http_001",
			Type:           contractsv1.EvidenceTypeTrace,
			Source:         probe.URL,
			Timestamp:      options.Now,
			Title:          "Laravel HTTP probe",
			Summary:        fmt.Sprintf("HTTP status was %d; Laravel error page signal was %t (%s).", probe.StatusCode, probe.LaravelErrorPage, probe.MatchedSignal),
			RawExcerpt:     safeExcerpt(probe.Excerpt),
			RedactionState: contractsv1.RedactionStateRedacted,
			RelatedIDs:     []string{},
			UIHints: contractsv1.UIHints{
				Icon:     "file-warning",
				Tone:     "danger",
				Sections: []string{"http", "evidence"},
			},
			ExternalRefs:  []contractsv1.ExternalRef{},
			KnowledgeRefs: []contractsv1.KnowledgeRef{},
		})
	}
	for index, issue := range issues {
		items = append(items, contractsv1.EvidenceItem{
			ID:             fmt.Sprintf("ev_laravel_issue_%03d", index+1),
			Type:           evidenceTypeForLaravelIssue(issue),
			Source:         defaultString(issue.Source, options.TargetDir),
			Timestamp:      options.Now,
			Title:          issue.Message,
			Summary:        laravelIssueSummary(issue),
			RawExcerpt:     safeExcerpt(defaultString(issue.Excerpt, issue.File)),
			RedactionState: contractsv1.RedactionStateRedacted,
			RelatedIDs:     []string{},
			UIHints: contractsv1.UIHints{
				Icon:     "alert-triangle",
				Tone:     "warning",
				Sections: []string{"laravel", "evidence"},
			},
			ExternalRefs:  []contractsv1.ExternalRef{},
			KnowledgeRefs: []contractsv1.KnowledgeRef{},
		})
	}
	if len(items) == 0 {
		items = append(items, contractsv1.EvidenceItem{
			ID:             "ev_laravel_issue_001",
			Type:           contractsv1.EvidenceTypeTrace,
			Source:         options.TargetDir,
			Timestamp:      options.Now,
			Title:          "Laravel failure signal",
			Summary:        "A Laravel failure was detected, but no log or HTTP excerpt was available.",
			RedactionState: contractsv1.RedactionStateNotNeeded,
			RelatedIDs:     []string{},
			UIHints: contractsv1.UIHints{
				Icon:     "alert-triangle",
				Tone:     "warning",
				Sections: []string{"laravel", "evidence"},
			},
			ExternalRefs:  []contractsv1.ExternalRef{},
			KnowledgeRefs: []contractsv1.KnowledgeRef{},
		})
	}
	return items
}

func evidenceTypeForLaravelIssue(issue LaravelIssue) contractsv1.EvidenceType {
	if strings.HasPrefix(issue.Kind, "missing_table") || strings.HasPrefix(issue.Kind, "missing_column") {
		return contractsv1.EvidenceTypeDB
	}
	if strings.Contains(issue.Source, ".log") {
		return contractsv1.EvidenceTypeLog
	}
	return contractsv1.EvidenceTypeTrace
}

func manualLaravelReviewRecommendation(issue LaravelIssue, reason string) contractsv1.RunbookRecommendation {
	return contractsv1.RunbookRecommendation{
		ID:         "rec_manual_laravel_review_001",
		Title:      "Escalate Laravel issue for manual patch",
		Reason:     reason + " Primary issue: " + laravelIssueSummary(issue) + ".",
		Confidence: 0.78,
		Steps: []string{
			"Review the recorded Laravel log or HTTP evidence.",
			"Identify the owning source file, migration, config, route, dependency, or permission change.",
			"Apply a targeted patch with an explicit rollback plan.",
			"Re-run the failing URL or command and confirm the Laravel error page is gone.",
		},
		RequiredPermissions: []string{"filesystem:read", "logs:read", "manual_patch:required"},
		EstimatedRisk:       contractsv1.SafetyBlocked,
		RequiresApproval:    true,
	}
}

func buildManualReviewRemediationPlan(options Options, diagnosisID string, issue LaravelIssue, reason string) contractsv1.RemediationPlan {
	return contractsv1.RemediationPlan{
		ID:                "rem_plan_laravel_manual_review_001",
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           "Automatic Laravel remediation blocked for " + laravelIssueSummary(issue) + ".",
		FixPreview: contractsv1.DiffPreview{
			Before: laravelIssueSummary(issue),
			After:  "No automatic change; manual patch required after review.",
		},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:                   "rollback_laravel_manual_review_001",
			RollbackType:         contractsv1.RollbackUnavailable,
			SnapshotRefs:         []string{},
			RestoreSteps:         []string{},
			Limitations:          []string{"No automatic patch was applied, so ai-logfixer has no generated change to roll back."},
			RiskLevel:            contractsv1.SafetyBlocked,
			RequiresManualReview: true,
		},
		RiskLevel:        contractsv1.SafetyBlocked,
		ApprovalRequired: true,
		Status:           contractsv1.RemediationStatusEscalated,
		DisplayStatus:    "Automatic fix blocked",
		UserMessage:      reason + " The target was left unchanged.",
		NextActions:      []contractsv1.NextAction{{ID: "next_manual_laravel_patch", Label: "Prepare manual patch", ActionType: "manual_review", Description: "Use the classification and evidence to author a specific patch.", Enabled: true}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: "tl_laravel_manual_plan_001", Type: "remediation.escalated", Message: "Automatic Laravel remediation blocked by safety policy.", Severity: "warning", Timestamp: options.Now}},
		ExternalRefs:     []contractsv1.ExternalRef{},
		KnowledgeRefs:    []contractsv1.KnowledgeRef{},
		CreatedAt:        options.Now,
	}
}

func buildEscalatedAttempt(options Options, planID string, reason string) contractsv1.RemediationAttempt {
	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)
	return contractsv1.RemediationAttempt{
		ID:                  "rem_attempt_laravel_manual_review_001",
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "manual_review_required",
		Status:              contractsv1.RemediationStatusEscalated,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary:      contractsv1.MonitorSummary{Status: "not_applied", Message: reason, Signals: []string{"automatic_fix_blocked"}, Duration: "0s"},
		DisplayStatus:       "Escalated without changes",
		UserMessage:         reason + " No files, database schema, or config were changed.",
		TimelineEvents:      []contractsv1.TimelineEvent{{ID: "tl_laravel_manual_attempt_001", Type: "remediation.escalated", Message: "Laravel issue requires manual remediation.", Severity: "warning", Timestamp: finished}},
		ExternalRefs:        []contractsv1.ExternalRef{},
	}
}

func buildEscalatedReceipt(options Options, diagnosisID string, planID string, attemptID string, issue LaravelIssue, reason string) contractsv1.Receipt {
	return contractsv1.Receipt{
		ID:                   "receipt_laravel_manual_review_001",
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attemptID,
		ActionTaken:          "classified Laravel issue; no automatic patch applied",
		Actor:                "ai-logfixer-laravel",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          laravelIssueSummary(issue),
		AfterState:           "unchanged; manual review required",
		Outcome:              "escalated",
		Summary:              reason + " AI LogFixer recorded a blocked remediation receipt for " + laravelIssueSummary(issue) + ".",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: "tl_laravel_manual_receipt_001", Type: "receipt.created", Message: "Receipt recorded for blocked Laravel remediation.", Severity: "warning", Timestamp: options.Now.Add(3 * time.Second)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
}

func laravelIssueSummary(issue LaravelIssue) string {
	if issue.Subject == "" {
		return issue.Message
	}
	return issue.Message + ": " + issue.Subject
}

func missingClassList(missing []MissingClass) string {
	classes := make([]string, 0, len(missing))
	for _, item := range missing {
		classes = append(classes, item.Class)
	}
	sort.Strings(classes)
	return strings.Join(classes, ", ")
}

func inferredMethodList(methods []InferredMethod) string {
	if len(methods) == 0 {
		return "magic __call/__callStatic fallback only"
	}
	names := make([]string, 0, len(methods))
	for _, method := range methods {
		mode := "instance"
		if method.Static && !method.Instance {
			mode = "static"
		} else if method.Static && method.Instance {
			mode = "static+instance"
		}
		names = append(names, method.Name+"("+mode+")")
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var unique []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func safeExcerpt(content string) string {
	content = strings.ReplaceAll(content, "\x00", "")
	content = strings.Join(strings.Fields(content), " ")
	if len(content) > 1200 {
		return content[:1200]
	}
	return content
}

func tailString(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[len(value)-max:]
}

const genericMagicFallbackSource = `    public function __call($name, $args)
    {
        return self::defaultReturn($name, $args);
    }

    public static function __callStatic($name, $args)
    {
        return self::defaultReturn($name, $args);
    }

    private static function defaultReturn($method, array $args = [])
    {
        $method = strtolower((string) $method);

        if (preg_match('/^(is|has|can|should)|enabled|exists|valid/', $method)) {
            return false;
        }

        if (str_contains($method, 'count') || str_contains($method, 'total') || str_contains($method, 'score') || str_contains($method, 'amount')) {
            return 0;
        }

        if (str_contains($method, 'render') || str_contains($method, 'format') || str_contains($method, 'html') || str_contains($method, 'text') || str_contains($method, 'label') || str_contains($method, 'message')) {
            $value = $args[0] ?? '';
            if (function_exists('e')) {
                return e((string) $value);
            }
            return htmlspecialchars((string) $value, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');
        }

        if (str_contains($method, 'parse') || str_contains($method, 'extract') || str_contains($method, 'map') || str_contains($method, 'filter')) {
            return [];
        }

        if (str_contains($method, 'search') || str_contains($method, 'list') || str_contains($method, 'all')) {
            if (function_exists('collect')) {
                return collect();
            }
            return [];
        }

        return null;
    }

`
