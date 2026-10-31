package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runnerFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"internal/subject", "tests/_suites/unit/subject"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range map[string]string{
		"go.mod":                      "module fixture\n",
		"internal/subject/subject.go": "package subject\n",
		"tests/_suites/unit/subject/subject_test.go": "//go:build integration\n\npackage subject\n",
		"tests/layout.json":                          `{"version":1,"groups":{"unit/subject":"internal/subject"}}`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRunnerOverlayPreservesPackageAndBuildTag(t *testing.T) {
	root := runnerFixture(t)
	got, err := buildOverlay(root)
	if err != nil {
		t.Fatal(err)
	}
	virtual := filepath.Join(root, "internal/subject/subject_test.go")
	want := filepath.Join(root, "tests/_suites/unit/subject/subject_test.go")
	if len(got.Replace) != 1 || got.Replace[virtual] != want {
		t.Fatal(got)
	}
	if _, err := os.Stat(virtual); !os.IsNotExist(err) {
		t.Fatal("runner created a test file beside production code")
	}
	data, err := os.ReadFile(want)
	if err != nil || !strings.HasPrefix(string(data), "//go:build integration\n") {
		t.Fatal("build tag changed", err)
	}
}

func TestRunnerRejectsMissingAndAmbiguousSuites(t *testing.T) {
	for _, scenario := range []string{"unmapped", "duplicate", "outside", "empty", "traversal", "symlink", "collision", "helper"} {
		t.Run(scenario, func(t *testing.T) {
			root := runnerFixture(t)
			config := layout{Version: 1, Groups: map[string]string{"unit/subject": "internal/subject"}}
			write := func(name, content string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "unmapped":
				write("tests/_suites/unknown/extra_test.go", "package subject\n")
			case "duplicate":
				config.Groups["integration"] = "internal/subject"
				write("tests/_suites/integration/subject_test.go", "package subject\n")
			case "outside":
				write("internal/subject/forgotten_test.go", "package subject\n")
			case "empty":
				config.Groups["missing"] = "internal/subject"
			case "traversal":
				config.Groups["unit/subject"] = "../escape"
			case "symlink":
				if err := os.Symlink(filepath.Join(root, "go.mod"), filepath.Join(root, "tests/_suites/unit/subject/link_test.go")); err != nil {
					t.Fatal(err)
				}
			case "collision":
				write("internal/subject/subject_test.go", "package subject\n")
			case "helper":
				write("tests/_suites/unit/subject/helper.go", "package subject\n")
			}
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			write("tests/layout.json", string(data))
			if _, err := buildOverlay(root); err == nil {
				t.Fatal("invalid layout silently accepted")
			}
		})
	}
}

func TestRunnerRootDiscoveryResolvesSymlinks(t *testing.T) {
	root := runnerFixture(t)
	alias := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	got, err := projectRoot(filepath.Join(alias, "internal", "subject"))
	want, resolveErr := filepath.EvalSymlinks(root)
	if err != nil || resolveErr != nil || got != want {
		t.Fatal("overlay paths must use canonical checkout paths", got, err)
	}
}
