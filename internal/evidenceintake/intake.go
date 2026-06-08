package evidenceintake

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func BuildBundle(request Request) (Bundle, error) {
	if err := request.Validate(); err != nil {
		return Bundle{}, err
	}

	appRoot := filepath.Clean(request.AppRoot)
	capturedAt := request.CapturedAt.UTC()
	bundle := Bundle{
		SchemaVersion: SchemaVersion,
		AppRoot:       appRoot,
		Source:        defaultString(request.Source, "unknown"),
		CapturedAt:    capturedAt,
		Process:       normalizeProcess(request.Process, appRoot),
		Items:         []Item{},
	}

	if hasProcess(request.Process) {
		bundle.Items = append(bundle.Items, processItem(bundle.Process, appRoot, capturedAt))
	}
	for _, sample := range request.Logs {
		bundle.Items = append(bundle.Items, logItem(sample, appRoot, capturedAt))
	}
	for _, probe := range request.Probes {
		bundle.Items = append(bundle.Items, probeItem(probe, capturedAt))
	}
	for _, snapshot := range request.ConfigSnapshots {
		bundle.Items = append(bundle.Items, configItem(snapshot, appRoot, capturedAt))
	}
	for _, manifest := range request.PackageManifests {
		bundle.Items = append(bundle.Items, packageManifestItem(manifest, appRoot, capturedAt))
	}
	for _, permission := range request.Permissions {
		bundle.Items = append(bundle.Items, permissionItem(permission, appRoot, capturedAt))
	}
	for _, dependency := range request.Dependencies {
		bundle.Items = append(bundle.Items, dependencyItem(dependency, capturedAt))
	}
	for _, service := range request.Services {
		bundle.Items = append(bundle.Items, serviceItem(service, capturedAt))
	}

	bundle.Summary = summarize(bundle.Items, request, appRoot)
	return bundle, bundle.Validate()
}

func processItem(process ProcessMetadata, appRoot string, capturedAt time.Time) Item {
	source := defaultString(process.CWD, appRoot)
	return newItem(KindProcess, source, capturedAt, "Process metadata", "Captured process identity and execution context.", processRaw(process), nil)
}

func logItem(sample LogSample, appRoot string, capturedAt time.Time) Item {
	source := normalizeSource(appRoot, sample.Source)
	timestamp := defaultTime(sample.Timestamp, capturedAt)
	content := strings.Join(sample.Lines, "\n")
	return newItem(KindLog, source, timestamp, "Log sample", "Captured application log lines for the incident window.", content, nil)
}

func probeItem(probe ProbeResult, capturedAt time.Time) Item {
	timestamp := defaultTime(probe.Timestamp, capturedAt)
	summary := fmt.Sprintf("Probe %q returned status %q.", probe.Name, probe.Status)
	if probe.StatusCode > 0 {
		summary = fmt.Sprintf("Probe %q returned status %q with HTTP %d.", probe.Name, probe.Status, probe.StatusCode)
	}
	metadata := map[string]string{
		"name":   probe.Name,
		"target": probe.Target,
		"status": probe.Status,
	}
	if probe.StatusCode > 0 {
		metadata["status_code"] = strconv.Itoa(probe.StatusCode)
	}
	if probe.Latency > 0 {
		metadata["latency_ms"] = strconv.FormatInt(probe.Latency.Milliseconds(), 10)
	}
	raw := strings.TrimSpace(strings.Join([]string{
		"name=" + probe.Name,
		"target=" + probe.Target,
		"status=" + probe.Status,
		"status_code=" + strconv.Itoa(probe.StatusCode),
		"output=" + probe.Output,
	}, " "))
	return newItem(KindProbe, probe.Target, timestamp, "Probe result", summary, raw, metadata)
}

func configItem(snapshot ConfigSnapshot, appRoot string, capturedAt time.Time) Item {
	source := normalizeSource(appRoot, snapshot.Path)
	timestamp := defaultTime(snapshot.Timestamp, capturedAt)
	metadata := map[string]string{"path": source}
	if snapshot.Format != "" {
		metadata["format"] = snapshot.Format
	}
	return newItem(KindConfigSnapshot, source, timestamp, "Config snapshot", "Captured configuration content for incident analysis.", snapshot.Content, metadata)
}

func packageManifestItem(manifest PackageManifest, appRoot string, capturedAt time.Time) Item {
	source := normalizeSource(appRoot, manifest.Path)
	timestamp := defaultTime(manifest.Timestamp, capturedAt)
	metadata := map[string]string{
		"path":          source,
		"manager":       manifest.Manager,
		"package_count": strconv.Itoa(len(manifest.Packages)),
	}
	return newItem(KindPackageManifest, source, timestamp, "Package manifest", "Captured package manager manifest and declared dependency versions.", packageManifestRaw(manifest), metadata)
}

func permissionItem(permission PermissionState, appRoot string, capturedAt time.Time) Item {
	source := normalizeSource(appRoot, permission.Path)
	timestamp := defaultTime(permission.Timestamp, capturedAt)
	metadata := map[string]string{
		"path":     source,
		"owner":    permission.Owner,
		"group":    permission.Group,
		"mode":     permission.Mode,
		"readable": strconv.FormatBool(permission.Readable),
		"writable": strconv.FormatBool(permission.Writable),
	}
	return newItem(KindPermission, source, timestamp, "Permission state", "Captured file ownership, mode, and access probe result.", permissionRaw(permission, source), metadata)
}

func dependencyItem(dependency DependencyState, capturedAt time.Time) Item {
	timestamp := defaultTime(dependency.Timestamp, capturedAt)
	metadata := map[string]string{
		"name":   dependency.Name,
		"kind":   dependency.Kind,
		"status": dependency.Status,
	}
	raw := strings.TrimSpace(strings.Join([]string{
		"name=" + dependency.Name,
		"kind=" + dependency.Kind,
		"status=" + dependency.Status,
		"detail=" + dependency.Detail,
	}, " "))
	return newItem(KindDependency, dependency.Name, timestamp, "Dependency state", "Captured dependency or backing-service health.", raw, metadata)
}

func serviceItem(service ServiceState, capturedAt time.Time) Item {
	timestamp := defaultTime(service.Timestamp, capturedAt)
	metadata := map[string]string{
		"name":   service.Name,
		"status": service.Status,
	}
	raw := strings.TrimSpace(strings.Join([]string{
		"name=" + service.Name,
		"status=" + service.Status,
		"detail=" + service.Detail,
	}, " "))
	return newItem(KindService, service.Name, timestamp, "Service state", "Captured runtime service status.", raw, metadata)
}

func newItem(kind Kind, source string, timestamp time.Time, title string, summary string, raw string, metadata map[string]string) Item {
	redacted, state := redactWithState(raw)
	return Item{
		ID:             stableID(kind, source, timestamp, title),
		Kind:           kind,
		Source:         source,
		Timestamp:      timestamp.UTC(),
		Title:          title,
		Summary:        summary,
		RawExcerpt:     redacted,
		RedactionState: state,
		Metadata:       compactMetadata(metadata),
	}
}

func summarize(items []Item, request Request, appRoot string) Summary {
	summary := Summary{
		TotalItems:            len(items),
		CountsByKind:          map[Kind]int{},
		FailingProbes:         []string{},
		UnhealthyDependencies: []string{},
		UnhealthyServices:     []string{},
		UnwritablePaths:       []string{},
	}
	for _, item := range items {
		summary.CountsByKind[item.Kind]++
		if item.RedactionState == RedactionRedacted {
			summary.RedactedItems++
		}
	}
	for _, probe := range request.Probes {
		if !healthyStatus(probe.Status) {
			summary.FailingProbes = append(summary.FailingProbes, probe.Name)
		}
	}
	for _, dependency := range request.Dependencies {
		if !healthyStatus(dependency.Status) {
			summary.UnhealthyDependencies = append(summary.UnhealthyDependencies, dependency.Name)
		}
	}
	for _, service := range request.Services {
		if !healthyStatus(service.Status) {
			summary.UnhealthyServices = append(summary.UnhealthyServices, service.Name)
		}
	}
	for _, permission := range request.Permissions {
		if !permission.Writable {
			summary.UnwritablePaths = append(summary.UnwritablePaths, normalizeSource(appRoot, permission.Path))
		}
	}
	return summary
}

func normalizeProcess(process ProcessMetadata, appRoot string) ProcessMetadata {
	process.CWD = normalizeSource(appRoot, process.CWD)
	return process
}

func hasProcess(process ProcessMetadata) bool {
	return process.PID != 0 ||
		process.Command != "" ||
		process.Executable != "" ||
		process.CWD != "" ||
		process.User != "" ||
		process.Environment != ""
}

func processRaw(process ProcessMetadata) string {
	parts := []string{
		"pid=" + strconv.Itoa(process.PID),
		"command=" + process.Command,
		"executable=" + process.Executable,
		"cwd=" + process.CWD,
		"user=" + process.User,
		"environment=" + process.Environment,
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func packageManifestRaw(manifest PackageManifest) string {
	lines := []string{
		"manager=" + manifest.Manager,
		"path=" + manifest.Path,
	}
	for _, dependency := range manifest.Packages {
		if dependency.Version == "" {
			lines = append(lines, dependency.Name)
			continue
		}
		lines = append(lines, dependency.Name+"@"+dependency.Version)
	}
	return strings.Join(lines, "\n")
}

func permissionRaw(permission PermissionState, source string) string {
	return strings.TrimSpace(strings.Join([]string{
		"path=" + source,
		"owner=" + permission.Owner,
		"group=" + permission.Group,
		"mode=" + permission.Mode,
		"readable=" + strconv.FormatBool(permission.Readable),
		"writable=" + strconv.FormatBool(permission.Writable),
	}, " "))
}

func normalizeSource(appRoot string, source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if hasScheme(source) {
		return source
	}
	if filepath.IsAbs(source) {
		return filepath.Clean(source)
	}
	return filepath.Clean(filepath.Join(appRoot, source))
}

func compactMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if key != "" && value != "" {
			out[key] = value
		}
	}
	return out
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultTime(value time.Time, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback.UTC()
	}
	return value.UTC()
}

func healthyStatus(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active", "available", "healthy", "ok", "pass", "passing", "reachable", "ready", "running", "success", "succeeded", "up":
		return true
	default:
		return false
	}
}

func hasScheme(value string) bool {
	return strings.Contains(value, "://")
}

func stableID(kind Kind, source string, timestamp time.Time, title string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{string(kind), source, timestamp.UTC().Format(time.RFC3339Nano), title}, "\x00")))
	return fmt.Sprintf("ev_%s_%x", kind, sum[:6])
}
