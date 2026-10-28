package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codefly-dev/core/shared"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// npmBuildInputs admits only package roots and archives named by the npm graph.
// The common ancestor supplies coordinates; it is never itself a copy request.
type npmBuildInputs struct {
	roots, archives, installs []string
	common                    string
}

func declaredBuildInputs(app string) (*npmBuildInputs, error) {
	inputs := &npmBuildInputs{common: app}
	state := map[string]int{}
	var visit func(string, bool) error
	visit = func(root string, installedByParent bool) error {
		if state[root] == 1 {
			return fmt.Errorf("local npm dependency cycle at %s", root)
		}
		if state[root] == 2 {
			return nil
		}
		state[root] = 1
		manifest, err := readNodePackageManifest(root)
		if err != nil {
			return err
		}
		if manifest == nil {
			return fmt.Errorf("missing package.json: %s", root)
		}
		lock, err := readNodePackageLock(root)
		if err != nil {
			return err
		}
		if lock == nil && (root == app || (!installedByParent && len(manifest.Dependencies)+len(manifest.DevDependencies)+len(manifest.OptionalDependencies) > 0)) {
			return fmt.Errorf("package with install dependencies requires package-lock.json: %s", root)
		}
		refs := map[string]bool{}
		for _, deps := range []map[string]string{manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies, manifest.PeerDependencies} {
			for _, ref := range deps {
				switch {
				case strings.HasPrefix(ref, "file:"):
					refs[strings.TrimPrefix(ref, "file:")] = true
				case filepath.IsAbs(ref), strings.HasPrefix(ref, "."), strings.HasPrefix(ref, "link:"), strings.HasPrefix(ref, "workspace:"):
					return fmt.Errorf("unsupported local npm reference %q in %s; use relative file: declarations", ref, root)
				}
			}
		}
		if lock != nil {
			for _, entry := range lock.Packages {
				if entry.Link || strings.HasPrefix(entry.Resolved, "file:") {
					refs[strings.TrimPrefix(entry.Resolved, "file:")] = true
				}
			}
		}
		names := make([]string, 0, len(refs))
		for name := range refs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, ref := range names {
			if ref == "" || filepath.IsAbs(ref) || strings.ContainsAny(ref, "\\:\x00") {
				return fmt.Errorf("unsupported local npm path %q in %s", ref, root)
			}
			target := filepath.Clean(filepath.Join(root, filepath.FromSlash(ref)))
			physical, err := filepath.EvalSymlinks(target)
			if err != nil {
				return fmt.Errorf("npm input %s: %w", target, err)
			}
			if physical != target {
				return fmt.Errorf("symlink npm input refused: %s", target)
			}
			info, err := os.Stat(target)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := visit(target, lock != nil && containedIn(root, target)); err != nil {
					return err
				}
			} else if info.Mode().IsRegular() && (strings.HasSuffix(target, ".tgz") || strings.HasSuffix(target, ".tar.gz")) {
				inputs.archives = append(inputs.archives, target)
			} else {
				return fmt.Errorf("npm input is not a package directory or archive: %s", target)
			}
		}
		state[root] = 2
		inputs.roots = append(inputs.roots, root)
		if lock != nil {
			inputs.installs = append(inputs.installs, root)
		}
		return nil
	}
	if err := visit(app, false); err != nil {
		return nil, err
	}
	for _, input := range append(append([]string{}, inputs.roots...), inputs.archives...) {
		for !containedIn(inputs.common, input) {
			inputs.common = filepath.Dir(inputs.common)
		}
	}
	if inputs.common == string(filepath.Separator) {
		return nil, fmt.Errorf("npm inputs have no bounded common directory")
	}
	return inputs, nil
}

// Each declared root keeps its Docker ignore policy. Matching is the same
// library used by Core's PrepareBuildContext; negations in one policy cannot
// override exclusions in another. Ancestor policy still applies to child roots.
type buildIgnore struct {
	root    string
	matcher *patternmatcher.PatternMatcher
}

func inputIgnorePolicies(roots []string) ([]buildIgnore, error) {
	var policies []buildIgnore
	for _, root := range roots {
		file, err := os.Open(filepath.Join(root, ".dockerignore"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		patterns, err := ignorefile.ReadAll(file)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		matcher, err := patternmatcher.New(patterns)
		if err != nil {
			return nil, err
		}
		policies = append(policies, buildIgnore{root, matcher})
	}
	return policies, nil
}

const contextOwner = "codefly-next-declared-inputs-v1\n"

func stageBuildInputs(ctx context.Context, app, service, output string) (*npmBuildInputs, string, error) {
	relative, err := filepath.Rel(service, app)
	if err != nil || !filepath.IsLocal(relative) {
		return nil, "", fmt.Errorf("application must stay inside service")
	}
	service, err = filepath.EvalSymlinks(service)
	if err != nil {
		return nil, "", err
	}
	expected := filepath.Join(service, relative)
	app, err = filepath.EvalSymlinks(expected)
	if err != nil {
		return nil, "", err
	}
	if app != expected {
		return nil, "", fmt.Errorf("symlink application source refused: %s", expected)
	}
	inputs, err := declaredBuildInputs(app)
	if err != nil {
		return nil, "", err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return nil, "", err
	}
	output = physicalTarget(output)
	for _, input := range append(append([]string{service}, inputs.roots...), inputs.archives...) {
		if containedIn(output, input) {
			return nil, "", fmt.Errorf("output directory contains source input: %s", input)
		}
	}
	for _, root := range inputs.roots {
		relative, _ := filepath.Rel(inputs.common, root)
		if strings.ContainsAny(relative, "$\\\n\r\x00") {
			return nil, "", fmt.Errorf("unsupported Docker workdir characters in %q", relative)
		}
	}
	destination := filepath.Join(output, "context")
	if info, statErr := os.Lstat(destination); statErr == nil {
		marker := filepath.Join(destination, ".codefly-owner")
		mi, me := os.Lstat(marker)
		data, re := os.ReadFile(marker)
		if !info.IsDir() || me != nil || !mi.Mode().IsRegular() || re != nil || string(data) != contextOwner {
			return nil, "", fmt.Errorf("refuse unowned generated context: %s", destination)
		}
	} else if !os.IsNotExist(statErr) {
		return nil, "", statErr
	}
	policies, err := inputIgnorePolicies(append([]string{service}, inputs.roots...))
	if err != nil {
		return nil, "", err
	}
	if err = os.MkdirAll(output, 0755); err != nil {
		return nil, "", err
	}
	staged, err := os.MkdirTemp(output, ".context-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(staged)
	copyInput := func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if containedIn(output, file) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch entry.Name() {
		case "node_modules", ".next", ".git", ".cache":
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case "tsconfig.tsbuildinfo":
			return nil
		}
		// Recognize host build artifacts only for exclusion, never output selection.
		if entry.IsDir() {
			if _, err := os.Lstat(filepath.Join(file, "required-server-files.json")); err == nil {
				return filepath.SkipDir
			}
		}
		for _, policy := range policies {
			if containedIn(policy.root, file) && file != policy.root {
				rel, _ := filepath.Rel(policy.root, file)
				excluded, err := policy.matcher.MatchesOrParentMatches(filepath.ToSlash(rel))
				if err != nil {
					return err
				}
				if excluded {
					return nil
				}
			}
		}
		relative, _ := filepath.Rel(inputs.common, file)
		target := filepath.Join(staged, "src", relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular npm build input refused: %s", file)
		}
		if err = shared.CopyFile(ctx, file, target); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	}
	for _, root := range inputs.roots {
		if err = filepath.WalkDir(root, copyInput); err != nil {
			return nil, "", err
		}
	}
	for _, archive := range inputs.archives {
		if err = filepath.WalkDir(archive, copyInput); err != nil {
			return nil, "", err
		}
	}
	// Ignore policies may not silently discard an admitted install input.
	for _, root := range inputs.roots {
		rel, _ := filepath.Rel(inputs.common, root)
		for _, name := range []string{"package.json", "package-lock.json", ".npmrc"} {
			if _, err := os.Stat(filepath.Join(root, name)); err == nil {
				if _, err = os.Stat(filepath.Join(staged, "src", rel, name)); err != nil {
					return nil, "", fmt.Errorf("ignored required npm input %s: %w", filepath.Join(root, name), err)
				}
			}
		}
	}
	for _, archive := range inputs.archives {
		rel, _ := filepath.Rel(inputs.common, archive)
		if _, err = os.Stat(filepath.Join(staged, "src", rel)); err != nil {
			return nil, "", fmt.Errorf("ignored npm archive %s", archive)
		}
	}
	if err = shared.CopyFile(ctx, filepath.Join(service, "service.codefly.yaml"), filepath.Join(staged, "service.codefly.yaml")); err != nil {
		return nil, "", err
	}
	if err = os.WriteFile(filepath.Join(staged, ".codefly-owner"), []byte(contextOwner), 0644); err != nil {
		return nil, "", err
	}
	if err = os.RemoveAll(destination); err != nil {
		return nil, "", err
	}
	if err = os.Rename(staged, destination); err != nil {
		return nil, "", err
	}
	relative, _ = filepath.Rel(inputs.common, app)
	return inputs, filepath.ToSlash(relative), nil
}
