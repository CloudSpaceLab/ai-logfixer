package evidenceintake

import "time"

const SchemaVersion = "incident-evidence-intake/v1"

type Kind string

const (
	KindProcess         Kind = "process"
	KindLog             Kind = "log"
	KindProbe           Kind = "probe"
	KindConfigSnapshot  Kind = "config_snapshot"
	KindPackageManifest Kind = "package_manifest"
	KindPermission      Kind = "permission"
	KindDependency      Kind = "dependency"
	KindService         Kind = "service"
)

type RedactionState string

const (
	RedactionRedacted  RedactionState = "redacted"
	RedactionNotNeeded RedactionState = "not_needed"
)

type Request struct {
	AppRoot          string            `json:"app_root"`
	Source           string            `json:"source"`
	CapturedAt       time.Time         `json:"captured_at"`
	Process          ProcessMetadata   `json:"process"`
	Logs             []LogSample       `json:"logs"`
	Probes           []ProbeResult     `json:"probes"`
	ConfigSnapshots  []ConfigSnapshot  `json:"config_snapshots"`
	PackageManifests []PackageManifest `json:"package_manifests"`
	Permissions      []PermissionState `json:"permissions"`
	Dependencies     []DependencyState `json:"dependencies"`
	Services         []ServiceState    `json:"services"`
}

type ProcessMetadata struct {
	PID         int    `json:"pid"`
	Command     string `json:"command"`
	Executable  string `json:"executable"`
	CWD         string `json:"cwd"`
	User        string `json:"user"`
	Environment string `json:"environment"`
}

type LogSample struct {
	Source    string    `json:"source"`
	Timestamp time.Time `json:"timestamp"`
	Lines     []string  `json:"lines"`
}

type ProbeResult struct {
	Name       string        `json:"name"`
	Target     string        `json:"target"`
	Status     string        `json:"status"`
	StatusCode int           `json:"status_code"`
	Latency    time.Duration `json:"latency"`
	Output     string        `json:"output"`
	Timestamp  time.Time     `json:"timestamp"`
}

type ConfigSnapshot struct {
	Path      string    `json:"path"`
	Format    string    `json:"format"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type PackageManifest struct {
	Path      string              `json:"path"`
	Manager   string              `json:"manager"`
	Packages  []PackageDependency `json:"packages"`
	Timestamp time.Time           `json:"timestamp"`
}

type PackageDependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type PermissionState struct {
	Path      string    `json:"path"`
	Owner     string    `json:"owner"`
	Group     string    `json:"group"`
	Mode      string    `json:"mode"`
	Readable  bool      `json:"readable"`
	Writable  bool      `json:"writable"`
	Timestamp time.Time `json:"timestamp"`
}

type DependencyState struct {
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail"`
	Timestamp time.Time `json:"timestamp"`
}

type ServiceState struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail"`
	Timestamp time.Time `json:"timestamp"`
}

type Bundle struct {
	SchemaVersion string          `json:"schema_version"`
	AppRoot       string          `json:"app_root"`
	Source        string          `json:"source"`
	CapturedAt    time.Time       `json:"captured_at"`
	Process       ProcessMetadata `json:"process"`
	Items         []Item          `json:"items"`
	Summary       Summary         `json:"summary"`
}

type Item struct {
	ID             string            `json:"id"`
	Kind           Kind              `json:"kind"`
	Source         string            `json:"source"`
	Timestamp      time.Time         `json:"timestamp"`
	Title          string            `json:"title"`
	Summary        string            `json:"summary"`
	RawExcerpt     string            `json:"raw_excerpt"`
	RedactionState RedactionState    `json:"redaction_state"`
	Metadata       map[string]string `json:"metadata"`
}

type Summary struct {
	TotalItems            int          `json:"total_items"`
	CountsByKind          map[Kind]int `json:"counts_by_kind"`
	RedactedItems         int          `json:"redacted_items"`
	FailingProbes         []string     `json:"failing_probes"`
	UnhealthyDependencies []string     `json:"unhealthy_dependencies"`
	UnhealthyServices     []string     `json:"unhealthy_services"`
	UnwritablePaths       []string     `json:"unwritable_paths"`
}
