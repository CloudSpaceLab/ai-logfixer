package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type IncidentSignal struct {
	Service     string
	Source      string
	Kind        string
	Method      string
	Route       string
	StatusCode  int
	StatusClass int
	Code        string
	Signature   string
	Count       int
	Start       time.Time
	End         time.Time
	RawExcerpts []string
	Tags        []string
}

type EvidenceBundle struct {
	Items []EvidenceItem
}

type EvidenceItem struct {
	ID      string
	Source  string
	Title   string
	Summary string
	Excerpt string
}

type RemediationCandidate struct {
	ID               string
	Summary          string
	TargetRefs       []string
	Before           string
	After            string
	Risk             string
	RequiresApproval bool
	BlockedReasons   []string
}

type HTTPLogEntry struct {
	Timestamp time.Time
	Service   string
	Method    string
	Route     string
	Status    int
	Source    string
	Raw       string
}

type FailureThreshold struct {
	ServiceName string
	Method      string
	Route       string
	StatusCode  int
	StatusClass int
	MinCount    int
	Window      time.Duration
}

func (s IncidentSignal) ErrorCode() string {
	if s.Code != "" {
		return s.Code
	}
	if s.StatusCode > 0 {
		return strconv.Itoa(s.StatusCode)
	}
	if s.StatusClass > 0 {
		return strconv.Itoa(s.StatusClass)
	}
	if s.Signature != "" {
		return s.Signature
	}
	return "unknown"
}

func (s IncidentSignal) RouteLabel() string {
	if s.Method != "" && s.Route != "" {
		return s.Method + " " + s.Route
	}
	if s.Route != "" {
		return s.Route
	}
	if s.Signature != "" {
		return s.Signature
	}
	return s.Kind
}

func (s IncidentSignal) StableParts() []string {
	return []string{
		s.Service,
		s.Source,
		s.Kind,
		s.Method,
		s.Route,
		strconv.Itoa(s.StatusCode),
		strconv.Itoa(s.StatusClass),
		s.Code,
		s.Signature,
		s.Start.UTC().Format(time.RFC3339Nano),
		s.End.UTC().Format(time.RFC3339Nano),
	}
}

func ParseKeyValueHTTPLogs(content string, source string) []HTTPLogEntry {
	var entries []HTTPLogEntry
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		values := parseKeyValues(line)
		status, _ := strconv.Atoi(values["status"])
		if status == 0 {
			continue
		}
		timestamp := parseLineTimestamp(line)
		entries = append(entries, HTTPLogEntry{
			Timestamp: timestamp,
			Service:   values["service"],
			Method:    strings.ToUpper(values["method"]),
			Route:     values["route"],
			Status:    status,
			Source:    source,
			Raw:       line,
		})
	}
	return entries
}

func RepeatedHTTPFailures(entries []HTTPLogEntry, threshold FailureThreshold) []IncidentSignal {
	if threshold.MinCount == 0 {
		threshold.MinCount = 3
	}
	if threshold.Window == 0 {
		threshold.Window = 5 * time.Minute
	}
	if threshold.StatusClass == 0 && threshold.StatusCode > 0 {
		threshold.StatusClass = (threshold.StatusCode / 100) * 100
	}

	byKey := map[string][]HTTPLogEntry{}
	for _, entry := range entries {
		if !matchesThreshold(entry, threshold) {
			continue
		}
		key := httpFailureKey(entry)
		byKey[key] = append(byKey[key], entry)
	}

	var signals []IncidentSignal
	for _, values := range byKey {
		sort.Slice(values, func(i, j int) bool {
			return values[i].Timestamp.Before(values[j].Timestamp)
		})
		for startIndex := range values {
			window := collectHTTPWindow(values[startIndex:], threshold.Window)
			if len(window) < threshold.MinCount {
				continue
			}
			first := window[0]
			last := window[len(window)-1]
			start, end := signalWindow(first.Timestamp, last.Timestamp)
			signals = append(signals, IncidentSignal{
				Service:     defaultString(threshold.ServiceName, first.Service),
				Source:      first.Source,
				Kind:        "http_failure",
				Method:      first.Method,
				Route:       first.Route,
				StatusCode:  first.Status,
				StatusClass: (first.Status / 100) * 100,
				Count:       len(window),
				Start:       start,
				End:         end,
				RawExcerpts: rawExcerpts(window, 8),
				Tags:        []string{"http", fmt.Sprintf("status_class=%d", (first.Status/100)*100)},
			})
			break
		}
	}

	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Count != signals[j].Count {
			return signals[i].Count > signals[j].Count
		}
		return signals[i].Start.Before(signals[j].Start)
	})
	return signals
}

func matchesThreshold(entry HTTPLogEntry, threshold FailureThreshold) bool {
	if entry.Status < 400 {
		return false
	}
	if threshold.ServiceName != "" && entry.Service != "" && entry.Service != threshold.ServiceName {
		return false
	}
	if threshold.Method != "" && entry.Method != "" && !strings.EqualFold(entry.Method, threshold.Method) {
		return false
	}
	if threshold.Route != "" && entry.Route != threshold.Route {
		return false
	}
	if threshold.StatusCode > 0 {
		return entry.Status == threshold.StatusCode
	}
	if threshold.StatusClass > 0 {
		return (entry.Status/100)*100 == threshold.StatusClass
	}
	return true
}

func collectHTTPWindow(entries []HTTPLogEntry, window time.Duration) []HTTPLogEntry {
	if len(entries) == 0 {
		return nil
	}
	if entries[0].Timestamp.IsZero() {
		return append([]HTTPLogEntry(nil), entries...)
	}
	start := entries[0].Timestamp
	var out []HTTPLogEntry
	for _, entry := range entries {
		if !entry.Timestamp.IsZero() && entry.Timestamp.Sub(start) > window {
			break
		}
		out = append(out, entry)
	}
	return out
}

func httpFailureKey(entry HTTPLogEntry) string {
	return fmt.Sprintf("%s|%s|%d", strings.ToUpper(entry.Method), entry.Route, (entry.Status/100)*100)
}

func rawExcerpts(entries []HTTPLogEntry, limit int) []string {
	if limit <= 0 || len(entries) <= limit {
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			out = append(out, entry.Raw)
		}
		return out
	}
	return rawExcerpts(entries[len(entries)-limit:], limit)
}

func parseLineTimestamp(line string) time.Time {
	firstField := line
	if index := strings.IndexAny(line, " \t"); index >= 0 {
		firstField = line[:index]
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, firstField)
		if err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func parseKeyValues(line string) map[string]string {
	re := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_-]*)=("[^"]*"|\S+)`)
	values := map[string]string{}
	for _, match := range re.FindAllStringSubmatch(line, -1) {
		values[strings.ToLower(match[1])] = strings.Trim(match[2], `"`)
	}
	return values
}

func signalWindow(start time.Time, end time.Time) (time.Time, time.Time) {
	if start.IsZero() {
		start = time.Now().UTC()
	}
	if end.IsZero() || !end.After(start) {
		end = start.Add(time.Nanosecond)
	}
	return start, end
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
