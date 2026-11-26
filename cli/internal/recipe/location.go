package recipe

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"quack/internal/config"
)

// Location selects project recipe files without changing the working directory of recipe commands.
type Location struct {
	Dir        string // The caller's working directory.
	ProjectDir string // Optional exact project root, relative to Dir or absolute.
}

// SelectionError identifies a project root or mapping that the caller must correct.
type SelectionError struct{ err error }

func (e *SelectionError) Error() string { return e.err.Error() }

func (e *SelectionError) Unwrap() error { return e.err }

// sources returns the selected .quack directory and the user's configuration as read for this operation.
// Initialization may create the .quack directory, but the root that contains it must exist.
func (l Location) sources(pkg string, initializing bool) (string, tomlFile, error) {
	start, err := filepath.Abs(l.Dir)
	if err != nil {
		return "", tomlFile{}, err
	}
	userPath, err := filepath.Abs(config.Path())
	if err != nil {
		return "", tomlFile{}, err
	}
	user, err := readTOML(userPath)
	if err != nil {
		return "", tomlFile{}, err
	}

	root, base, source := l.ProjectDir, start, "--project-dir"
	if root == "" {
		if found := findProject(start, home()); found != "" {
			return found, user, nil
		}
		if pkg == "" {
			return "", user, nil
		}
		root, err = user.projectFor(pkg)
		if err != nil {
			return "", user, &SelectionError{err}
		}
		if root == "" {
			if !initializing {
				return "", user, nil
			}
			root, source = start, "current directory"
		} else {
			base, source = filepath.Dir(userPath), userPath+": [projects]"
		}
	}
	project, err := exactProject(root, base, initializing)
	if err != nil {
		return "", user, &SelectionError{fmt.Errorf("%s: %w", source, err)}
	}
	return project, user, nil
}

func (f tomlFile) projectFor(pkg string) (string, error) {
	var decoded struct {
		Projects any `toml:"projects"`
	}
	if _, err := config.Decode(f.doc, f.path, false, &decoded); err != nil {
		return "", err
	}
	if decoded.Projects == nil {
		return "", nil
	}
	projects, ok := decoded.Projects.(map[string]any)
	if !ok {
		return "", fmt.Errorf("%s: [projects] must be a table mapping package patterns to project roots", f.path)
	}
	var matches []string
	for _, pattern := range slices.Sorted(maps.Keys(projects)) {
		re, err := packagePattern(pattern)
		if err != nil {
			return "", fmt.Errorf("%s: [projects] invalid pattern %q: %w", f.path, pattern, err)
		}
		if re.MatchString(pkg) {
			matches = append(matches, pattern)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		root, ok := projects[matches[0]].(string)
		if !ok || root == "" {
			return "", fmt.Errorf("%s: [projects] pattern %q must map to a nonempty project root path", f.path, matches[0])
		}
		return root, nil
	default:
		return "", fmt.Errorf("%s: [projects] is ambiguous: multiple patterns match %s: %q", f.path, pkg, matches)
	}
}

func exactProject(root, base string, initializing bool) (string, error) {
	switch {
	case root == "~" || strings.HasPrefix(root, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, strings.TrimPrefix(root, "~"))
	case strings.HasPrefix(root, "~"):
		return "", fmt.Errorf("unsupported project root %q: use ~ or ~/ for your home directory", root)
	case !filepath.IsAbs(root):
		root = filepath.Join(base, root)
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("project root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project root %s is not a directory", root)
	}
	for _, path := range []string{root, realPath(root)} {
		metadata, err := os.Stat(filepath.Join(filepath.Dir(path), projectDir))
		if err == nil && os.SameFile(info, metadata) {
			return "", fmt.Errorf("%s is the .quack directory: select its parent project root", root)
		}
	}
	project := filepath.Join(root, projectDir)
	info, err = os.Stat(project)
	if initializing && errors.Is(err, fs.ErrNotExist) {
		if _, linkErr := os.Lstat(project); errors.Is(linkErr, fs.ErrNotExist) {
			return project, nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("project directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", project)
	}
	return project, nil
}
