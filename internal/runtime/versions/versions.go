package versions

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Status string

const (
	StatusHealthy Status = "healthy"
	StatusDrift   Status = "version_mismatch_detected"
)

type Kind string

const (
	KindRuntime Kind = "runtime"
	KindPackage Kind = "package"
	KindAPI     Kind = "api"
)

type Options struct {
	ServiceName string
	Required    []Requirement
	Observed    []ObservedVersion
	Now         time.Time
	AllowRepair bool
}

type Requirement struct {
	Kind       Kind   `json:"kind"`
	Name       string `json:"name"`
	Constraint string `json:"constraint"`
}

type ObservedVersion struct {
	Kind    Kind   `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source,omitempty"`
}

type Result struct {
	ServiceName     string           `json:"service_name"`
	Status          Status           `json:"status"`
	Findings        []Finding        `json:"findings"`
	Recommendations []Recommendation `json:"recommendations"`
	AutoRepair      AutoRepair       `json:"auto_repair"`
	CheckedAt       time.Time        `json:"checked_at"`
}

type Finding struct {
	Kind       string `json:"kind"`
	TargetKind Kind   `json:"target_kind"`
	Name       string `json:"name"`
	Required   string `json:"required"`
	Observed   string `json:"observed,omitempty"`
	Source     string `json:"source,omitempty"`
	Message    string `json:"message"`
}

type Recommendation struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	Safe   bool   `json:"safe"`
}

type AutoRepair struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

func Diagnose(options Options) (Result, error) {
	options = normalizeOptions(options)
	if options.ServiceName == "" {
		return Result{}, errors.New("service name is required")
	}

	result := Result{
		ServiceName: options.ServiceName,
		Status:      StatusHealthy,
		CheckedAt:   options.Now,
		AutoRepair: AutoRepair{
			Allowed: false,
			Reason:  "version changes require a package manager, runtime manager, or API migration plan with verification",
		},
	}
	if options.AllowRepair {
		result.AutoRepair.Reason = "automatic repair remains disabled until an explicit upgrade/downgrade plan is supplied"
	}

	observed := observedIndex(options.Observed)
	for _, requirement := range options.Required {
		requirement = normalizeRequirement(requirement)
		if requirement.Name == "" || requirement.Constraint == "" {
			continue
		}
		key := versionKey(requirement.Kind, requirement.Name)
		actual, ok := observed[key]
		if !ok || strings.TrimSpace(actual.Version) == "" {
			result.Findings = append(result.Findings, Finding{
				Kind:       "missing_version_evidence",
				TargetKind: requirement.Kind,
				Name:       requirement.Name,
				Required:   requirement.Constraint,
				Message:    fmt.Sprintf("%s %q has no observed version evidence", requirement.Kind, requirement.Name),
			})
			continue
		}
		satisfied, err := satisfies(actual.Version, requirement.Constraint)
		if err != nil {
			result.Findings = append(result.Findings, Finding{
				Kind:       "unsupported_version_constraint",
				TargetKind: requirement.Kind,
				Name:       requirement.Name,
				Required:   requirement.Constraint,
				Observed:   actual.Version,
				Source:     actual.Source,
				Message:    err.Error(),
			})
			continue
		}
		if !satisfied {
			result.Findings = append(result.Findings, Finding{
				Kind:       "version_mismatch",
				TargetKind: requirement.Kind,
				Name:       requirement.Name,
				Required:   requirement.Constraint,
				Observed:   actual.Version,
				Source:     actual.Source,
				Message:    fmt.Sprintf("%s %q version %q does not satisfy %q", requirement.Kind, requirement.Name, actual.Version, requirement.Constraint),
			})
		}
	}
	if len(result.Findings) > 0 {
		result.Status = StatusDrift
	}
	result.Recommendations = recommendations(result.Findings)
	return result, nil
}

func normalizeOptions(options Options) Options {
	options.ServiceName = strings.TrimSpace(options.ServiceName)
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	return options
}

func normalizeRequirement(requirement Requirement) Requirement {
	requirement.Name = strings.ToLower(strings.TrimSpace(requirement.Name))
	requirement.Constraint = strings.TrimSpace(requirement.Constraint)
	if requirement.Kind == "" {
		requirement.Kind = KindPackage
	}
	return requirement
}

func observedIndex(observed []ObservedVersion) map[string]ObservedVersion {
	index := map[string]ObservedVersion{}
	for _, item := range observed {
		item.Name = strings.ToLower(strings.TrimSpace(item.Name))
		if item.Kind == "" {
			item.Kind = KindPackage
		}
		if item.Name != "" {
			index[versionKey(item.Kind, item.Name)] = item
		}
	}
	return index
}

func versionKey(kind Kind, name string) string {
	return string(kind) + ":" + strings.ToLower(strings.TrimSpace(name))
}

func satisfies(version string, constraint string) (bool, error) {
	parts := strings.Split(constraint, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ok, err := satisfiesOne(version, part)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func satisfiesOne(version string, constraint string) (bool, error) {
	operator := "="
	for _, candidate := range []string{">=", "<=", ">", "<", "=", "=="} {
		if strings.HasPrefix(constraint, candidate) {
			operator = candidate
			constraint = strings.TrimSpace(strings.TrimPrefix(constraint, candidate))
			break
		}
	}
	if strings.HasPrefix(constraint, "^") {
		base := strings.TrimPrefix(constraint, "^")
		observed, err := parseVersion(version)
		if err != nil {
			return false, err
		}
		required, err := parseVersion(base)
		if err != nil {
			return false, err
		}
		return observed.major == required.major && compareVersion(observed, required) >= 0, nil
	}

	observed, err := parseVersion(version)
	if err != nil {
		return false, err
	}
	required, err := parseVersion(constraint)
	if err != nil {
		return false, err
	}
	cmp := compareVersion(observed, required)
	switch operator {
	case "=", "==":
		return cmp == 0, nil
	case ">=":
		return cmp >= 0, nil
	case "<=":
		return cmp <= 0, nil
	case ">":
		return cmp > 0, nil
	case "<":
		return cmp < 0, nil
	default:
		return false, fmt.Errorf("unsupported version operator %q", operator)
	}
}

type semanticVersion struct {
	major int
	minor int
	patch int
}

func parseVersion(value string) (semanticVersion, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		return semanticVersion{}, errors.New("empty version")
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || unicode.IsSpace(r)
	})
	numbers := []int{}
	for _, field := range fields {
		if field == "" {
			continue
		}
		digits := leadingDigits(field)
		if digits == "" {
			break
		}
		number, err := strconv.Atoi(digits)
		if err != nil {
			return semanticVersion{}, err
		}
		numbers = append(numbers, number)
		if len(numbers) == 3 {
			break
		}
	}
	if len(numbers) == 0 {
		return semanticVersion{}, fmt.Errorf("version %q does not start with a numeric component", value)
	}
	for len(numbers) < 3 {
		numbers = append(numbers, 0)
	}
	return semanticVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}, nil
}

func leadingDigits(value string) string {
	for index, r := range value {
		if !unicode.IsDigit(r) {
			return value[:index]
		}
	}
	return value
}

func compareVersion(left semanticVersion, right semanticVersion) int {
	switch {
	case left.major != right.major:
		return left.major - right.major
	case left.minor != right.minor:
		return left.minor - right.minor
	default:
		return left.patch - right.patch
	}
}

func recommendations(findings []Finding) []Recommendation {
	if len(findings) == 0 {
		return []Recommendation{{Action: "no_version_change", Reason: "observed versions satisfy declared requirements", Safe: true}}
	}
	seen := map[string]bool{}
	var recommendations []Recommendation
	for _, finding := range findings {
		action := actionForKind(finding.TargetKind)
		if seen[action] {
			continue
		}
		seen[action] = true
		recommendations = append(recommendations, Recommendation{
			Action: action,
			Reason: finding.Message,
			Safe:   false,
		})
	}
	return recommendations
}

func actionForKind(kind Kind) string {
	switch kind {
	case KindRuntime:
		return "select_compatible_runtime_version"
	case KindAPI:
		return "update_api_version_or_client_contract"
	default:
		return "adjust_package_version_with_lockfile_verification"
	}
}
