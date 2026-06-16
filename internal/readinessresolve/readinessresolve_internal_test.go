package readinessresolve

import (
	"strings"
	"testing"
)

func TestNormalizePermissionTargetsDoesNotInheritPolicyIdentityForReadOnlyFiles(t *testing.T) {
	t.Parallel()

	targets, err := normalizePermissionTargets(permissionPolicy{
		ExpectedOwner: "app",
		ExpectedGroup: "app",
		PermissionTargets: []permissionTarget{
			{
				Path:         "public/status.txt",
				Kind:         "file",
				Access:       "read",
				ExpectedMode: "0644",
			},
			{
				Path:         "storage/audit.log",
				Kind:         "file",
				Access:       "write",
				ExpectedMode: "0664",
			},
			{
				Path:         "storage",
				Kind:         "dir",
				Access:       "write",
				ExpectedMode: "0775",
			},
		},
	})
	if err != nil {
		t.Fatalf("normalize permission targets: %v", err)
	}

	readOnly := targets[0]
	if readOnly.ExpectedOwner != "" || readOnly.ExpectedGroup != "" {
		t.Fatalf("read-only file target must not inherit runtime write identity, got owner=%q group=%q", readOnly.ExpectedOwner, readOnly.ExpectedGroup)
	}
	owner, group, shouldChown := permissionRepairIdentity(readOnly)
	if shouldChown || owner != "" || group != "" {
		t.Fatalf("read-only file repair must not chown by default, got owner=%q group=%q shouldChown=%t", owner, group, shouldChown)
	}

	writableFile := targets[1]
	if writableFile.ExpectedOwner != "app" || writableFile.ExpectedGroup != "app" {
		t.Fatalf("writable file target should inherit runtime identity, got owner=%q group=%q", writableFile.ExpectedOwner, writableFile.ExpectedGroup)
	}
	owner, group, shouldChown = permissionRepairIdentity(writableFile)
	if !shouldChown || owner != "app" || group != "app" {
		t.Fatalf("writable file repair should chown to runtime identity, got owner=%q group=%q shouldChown=%t", owner, group, shouldChown)
	}

	writableDir := targets[2]
	if writableDir.ExpectedOwner != "app" || writableDir.ExpectedGroup != "app" {
		t.Fatalf("writable dir target should inherit runtime identity, got owner=%q group=%q", writableDir.ExpectedOwner, writableDir.ExpectedGroup)
	}
}

func TestDockerPathSafetyScriptBlocksSymlinkEscapes(t *testing.T) {
	t.Parallel()

	script := dockerPathSafetyScript("storage/logs")
	for _, snippet := range []string{
		"candidate='/app/storage/logs'",
		"while [ ! -e \"$probe\" ]; do",
		"resolved=$(readlink -f \"$probe\")",
		"case \"$resolved\" in /app|/app/*)",
		"permission path escapes app root",
	} {
		if !strings.Contains(script, snippet) {
			t.Fatalf("docker path safety script missing %q:\n%s", snippet, script)
		}
	}
}
