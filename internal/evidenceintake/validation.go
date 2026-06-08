package evidenceintake

import (
	"errors"
	"fmt"
	"strings"
)

func (r Request) Validate() error {
	var errs []error

	require(&errs, strings.TrimSpace(r.AppRoot) != "", "app_root is required")
	require(&errs, !r.CapturedAt.IsZero(), "captured_at is required")
	require(&errs, r.Process.PID >= 0, "process.pid cannot be negative")
	require(&errs, hasEvidenceSource(r), "at least one evidence source is required")

	for index, log := range r.Logs {
		prefix := fmt.Sprintf("logs[%d]", index)
		require(&errs, strings.TrimSpace(log.Source) != "", prefix+".source is required")
		require(&errs, nonEmptyLines(log.Lines), prefix+".lines must not be empty")
	}
	for index, probe := range r.Probes {
		prefix := fmt.Sprintf("probes[%d]", index)
		require(&errs, strings.TrimSpace(probe.Name) != "", prefix+".name is required")
		require(&errs, strings.TrimSpace(probe.Target) != "", prefix+".target is required")
		require(&errs, strings.TrimSpace(probe.Status) != "", prefix+".status is required")
	}
	for index, snapshot := range r.ConfigSnapshots {
		prefix := fmt.Sprintf("config_snapshots[%d]", index)
		require(&errs, strings.TrimSpace(snapshot.Path) != "", prefix+".path is required")
		require(&errs, snapshot.Content != "", prefix+".content is required")
	}
	for index, manifest := range r.PackageManifests {
		prefix := fmt.Sprintf("package_manifests[%d]", index)
		require(&errs, strings.TrimSpace(manifest.Path) != "", prefix+".path is required")
		require(&errs, strings.TrimSpace(manifest.Manager) != "", prefix+".manager is required")
		for dependencyIndex, dependency := range manifest.Packages {
			require(&errs, strings.TrimSpace(dependency.Name) != "", fmt.Sprintf("%s.packages[%d].name is required", prefix, dependencyIndex))
		}
	}
	for index, permission := range r.Permissions {
		prefix := fmt.Sprintf("permissions[%d]", index)
		require(&errs, strings.TrimSpace(permission.Path) != "", prefix+".path is required")
		require(&errs, strings.TrimSpace(permission.Mode) != "", prefix+".mode is required")
	}
	for index, dependency := range r.Dependencies {
		prefix := fmt.Sprintf("dependencies[%d]", index)
		require(&errs, strings.TrimSpace(dependency.Name) != "", prefix+".name is required")
		require(&errs, strings.TrimSpace(dependency.Status) != "", prefix+".status is required")
	}
	for index, service := range r.Services {
		prefix := fmt.Sprintf("services[%d]", index)
		require(&errs, strings.TrimSpace(service.Name) != "", prefix+".name is required")
		require(&errs, strings.TrimSpace(service.Status) != "", prefix+".status is required")
	}

	return errors.Join(errs...)
}

func (b Bundle) Validate() error {
	var errs []error

	require(&errs, b.SchemaVersion == SchemaVersion, "schema_version must be "+SchemaVersion)
	require(&errs, strings.TrimSpace(b.AppRoot) != "", "app_root is required")
	require(&errs, !b.CapturedAt.IsZero(), "captured_at is required")
	require(&errs, b.Summary.TotalItems == len(b.Items), "summary.total_items must match item count")

	counts := map[Kind]int{}
	for index, item := range b.Items {
		errs = append(errs, prefixErr(fmt.Sprintf("items[%d]", index), item.Validate())...)
		counts[item.Kind]++
	}
	for kind, count := range counts {
		require(&errs, b.Summary.CountsByKind[kind] == count, fmt.Sprintf("summary.counts_by_kind[%s] must match item count", kind))
	}

	return errors.Join(errs...)
}

func (i Item) Validate() error {
	var errs []error

	require(&errs, strings.TrimSpace(i.ID) != "", "id is required")
	require(&errs, validKind(i.Kind), fmt.Sprintf("unsupported kind %q", i.Kind))
	require(&errs, strings.TrimSpace(i.Source) != "", "source is required")
	require(&errs, !i.Timestamp.IsZero(), "timestamp is required")
	require(&errs, strings.TrimSpace(i.Title) != "", "title is required")
	require(&errs, strings.TrimSpace(i.Summary) != "", "summary is required")
	require(&errs, validRedactionState(i.RedactionState), fmt.Sprintf("unsupported redaction_state %q", i.RedactionState))

	return errors.Join(errs...)
}

func nonEmptyLines(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func hasEvidenceSource(request Request) bool {
	return hasProcess(request.Process) ||
		len(request.Logs) > 0 ||
		len(request.Probes) > 0 ||
		len(request.ConfigSnapshots) > 0 ||
		len(request.PackageManifests) > 0 ||
		len(request.Permissions) > 0 ||
		len(request.Dependencies) > 0 ||
		len(request.Services) > 0
}

func validKind(kind Kind) bool {
	switch kind {
	case KindProcess, KindLog, KindProbe, KindConfigSnapshot, KindPackageManifest, KindPermission, KindDependency, KindService:
		return true
	default:
		return false
	}
}

func validRedactionState(state RedactionState) bool {
	switch state {
	case RedactionRedacted, RedactionNotNeeded:
		return true
	default:
		return false
	}
}

func require(errs *[]error, condition bool, message string) {
	if !condition {
		*errs = append(*errs, errors.New(message))
	}
}

func prefixErr(prefix string, err error) []error {
	if err == nil {
		return nil
	}
	return []error{fmt.Errorf("%s: %w", prefix, err)}
}
