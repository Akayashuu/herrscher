package core_test

import (
	"go/build"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorePurity asserts core/... never imports a concrete adapter (dctl) or any
// plugin. core is the agnostic engine: its dependency arrows point only at
// contracts and the standard library.
func TestCorePurity(t *testing.T) {
	forbidden := []string{
		"github.com/Herrscherd/dctl",
		"github.com/Herrscherd/herrscher-discord-gateway",
		"github.com/Herrscherd/herrscher-claude-backend",
	}
	roots := []string{
		"bridge", "config", "host", "service",
		"internal/control", "internal/forge", "internal/health",
		"internal/instanceid", "internal/manager", "internal/metrics",
		"internal/obs", "internal/state", "internal/supervisor",
		"internal/worktree",
	}
	for _, r := range roots {
		pkg, err := build.ImportDir(filepath.Join(".", r), 0)
		if err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		imports := append(append([]string{}, pkg.Imports...), pkg.TestImports...)
		imports = append(imports, pkg.XTestImports...)
		for _, imp := range imports {
			for _, bad := range forbidden {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					t.Errorf("core/%s imports forbidden %q", r, imp)
				}
			}
		}
	}
}
