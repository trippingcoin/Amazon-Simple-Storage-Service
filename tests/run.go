// The suites live under _suites so ordinary Go package discovery cannot compile
// them without their original package's private implementation. This command
// supplies that package context through the standard Go build overlay.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

type layout struct {
	Version int               `json:"version"`
	Groups  map[string]string `json:"groups"`
}

type overlay struct {
	Replace map[string]string
}

func projectRoot(start string) (string, error) {
	root, err := filepath.EvalSymlinks(start)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(root, "tests", "layout.json")); err == nil {
				return root, nil
			}
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", errors.New("run inside the project containing tests/layout.json")
		}
		root = parent
	}
}

func validRelative(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator))
}

func buildOverlay(root string) (overlay, error) {
	result := overlay{Replace: map[string]string{}}
	f, err := os.Open(filepath.Join(root, "tests", "layout.json"))
	if err != nil {
		return result, err
	}
	defer f.Close()
	var config layout
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return result, err
	}
	if config.Version != 1 || len(config.Groups) == 0 || decoder.Decode(new(any)) != io.EOF {
		return result, errors.New("invalid tests/layout.json")
	}
	used := map[string]bool{}
	for group, target := range config.Groups {
		if !validRelative(filepath.FromSlash(group)) || !validRelative(filepath.FromSlash(target)) || group == "." {
			return result, errors.New("test groups and packages must be relative paths inside the project")
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(target)))
		if err != nil || !info.IsDir() {
			return result, fmt.Errorf("missing target package for group %s", group)
		}
	}
	suites := filepath.Join(root, "tests", "_suites")
	err = filepath.WalkDir(suites, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("test suite must not be a symlink: %s", path)
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return fmt.Errorf("suite helpers must use _test.go: %s", path)
		}
		relative, err := filepath.Rel(suites, filepath.Dir(path))
		if err != nil {
			return err
		}
		group := filepath.ToSlash(relative)
		target, ok := config.Groups[group]
		if !ok {
			return fmt.Errorf("unmapped suite group: %s", group)
		}
		virtual := filepath.Join(root, filepath.FromSlash(target), entry.Name())
		if previous, exists := result.Replace[virtual]; exists {
			return fmt.Errorf("duplicate virtual test file: %s and %s", previous, path)
		}
		if _, err := os.Lstat(virtual); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("virtual test file already exists on disk: %s", virtual)
		}
		result.Replace[virtual] = path
		used[group] = true
		return nil
	})
	if err != nil {
		return result, err
	}
	for group := range config.Groups {
		if !used[group] {
			return result, fmt.Errorf("empty or missing suite group: %s", group)
		}
	}
	// Catch forgotten relocations instead of silently running only part of a suite.
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "tests" || entry.Name() == "vendor" || entry.Name() == "node_modules" || entry.Name() == "data" || entry.Name() == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			return fmt.Errorf("test outside tests/: %s", path)
		}
		return nil
	})
	return result, err
}

func run(args []string) int {
	verb := "test"
	if len(args) > 0 && args[0] == "vet" {
		verb, args = "vet", args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	for _, arg := range args {
		if arg == "-overlay" || strings.HasPrefix(arg, "-overlay=") || arg == "-C" || strings.HasPrefix(arg, "-C=") {
			fmt.Fprintln(os.Stderr, "tests: overlay and working directory are managed by this runner")
			return 1
		}
	}
	if len(args) == 0 {
		args = []string{"./..."}
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tests:", err)
		return 1
	}
	root, err := projectRoot(cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tests:", err)
		return 1
	}
	files, err := buildOverlay(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tests:", err)
		return 1
	}
	temp, err := os.MkdirTemp("", "triple-s-test-overlay-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tests:", err)
		return 1
	}
	defer os.RemoveAll(temp)
	name := filepath.Join(temp, "overlay.json")
	data, err := json.Marshal(files)
	if err == nil {
		err = os.WriteFile(name, data, 0600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tests:", err)
		return 1
	}
	command := exec.Command("go", append([]string{verb, "-overlay", name}, args...)...)
	command.Dir = root
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "tests:", err)
		return 1
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	defer close(done)
	defer signal.Stop(signals)
	go func() {
		for {
			select {
			case sig := <-signals:
				_ = command.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	if err := command.Wait(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:])) }
