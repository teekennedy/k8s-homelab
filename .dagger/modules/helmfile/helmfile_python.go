package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// pythonProjectFiles must be mounted next to a Python ConfigMap's scripts, so
// that uv builds the same environment the python module tests against.
var pythonProjectFiles = []string{".python-version", "pyproject.toml", "uv.lock"}

// uvRunFlags must all appear in the command of a container that runs a Python
// ConfigMap: uv must use the image's interpreter and the shipped lock as is.
var uvRunFlags = []string{"uv run", "--no-python-downloads", "--frozen"}

// uvImage matches a uv image with a bundled interpreter and captures its
// major.minor, e.g. ghcr.io/astral-sh/uv:python3.15-alpine@sha256:….
var uvImage = regexp.MustCompile(`^ghcr\.io/astral-sh/uv:python(\d+\.\d+)-`)

// Lints that Python ConfigMaps run as uv projects on their Python.
//
// Any container that mounts a ConfigMap carrying .py files must be able to see
// .python-version, pyproject.toml and uv.lock from it, run on a
// ghcr.io/astral-sh/uv:pythonX.Y image whose X.Y matches .python-version, and
// start with `uv run --frozen --no-python-downloads`. That way the pod runs on
// the Python and locked dependencies the python module tested.
// +check
func (r *Release) LintPython(ctx context.Context) error {
	contents, err := r.rendered().File("/rendered.yaml").Contents(ctx)
	if err != nil {
		return fmt.Errorf("helmfile template failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	problems, err := pythonRuntimeProblems([]byte(contents))
	if err != nil {
		return fmt.Errorf("reading rendered manifests for %s (%s): %w", r.Path, r.Environment, err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("python runtime problems in %s (%s):\n  %s", r.Path, r.Environment, strings.Join(problems, "\n  "))
	}
	return nil
}

type manifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec yaml.Node         `yaml:"spec"`
}

type podTemplate struct {
	Spec podSpec `yaml:"spec"`
}

type podSpec struct {
	InitContainers []container `yaml:"initContainers"`
	Containers     []container `yaml:"containers"`
	Volumes        []volume    `yaml:"volumes"`
}

type container struct {
	Name         string        `yaml:"name"`
	Image        string        `yaml:"image"`
	Command      []string      `yaml:"command"`
	Args         []string      `yaml:"args"`
	VolumeMounts []volumeMount `yaml:"volumeMounts"`
}

type volumeMount struct {
	Name    string `yaml:"name"`
	SubPath string `yaml:"subPath"`
}

type keyToPath struct {
	Key  string `yaml:"key"`
	Path string `yaml:"path"`
}

type configMapSource struct {
	Name  string      `yaml:"name"`
	Items []keyToPath `yaml:"items"`
}

type volume struct {
	Name      string           `yaml:"name"`
	ConfigMap *configMapSource `yaml:"configMap"`
	Projected *struct {
		Sources []struct {
			ConfigMap *configMapSource `yaml:"configMap"`
		} `yaml:"sources"`
	} `yaml:"projected"`
}

// workload is one pod spec in the rendered manifests, named for messages.
type workload struct {
	name string
	spec podSpec
}

// pythonRuntimeProblems returns one line per container that runs a Python
// ConfigMap in a way LintPython rejects.
func pythonRuntimeProblems(rendered []byte) ([]string, error) {
	configMaps := map[string]map[string]string{}
	var workloads []workload

	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	for {
		var m manifest
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decoding manifest: %w", err)
		}
		name := m.Kind + "/" + m.Metadata.Name
		if m.Kind == "ConfigMap" {
			configMaps[m.Metadata.Name] = m.Data
			continue
		}
		spec, ok, err := podSpecOf(m)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if ok {
			workloads = append(workloads, workload{name: name, spec: spec})
		}
	}

	var problems []string
	for _, w := range workloads {
		for _, c := range append(w.spec.InitContainers, w.spec.Containers...) {
			for _, p := range containerProblems(c, w.spec.Volumes, configMaps) {
				problems = append(problems, fmt.Sprintf("%s container %s: %s", w.name, c.Name, p))
			}
		}
	}
	return problems, nil
}

// podSpecOf returns the pod spec a manifest runs, if it runs one.
func podSpecOf(m manifest) (podSpec, bool, error) {
	var spec podSpec
	var template struct {
		Template podTemplate `yaml:"template"`
	}
	var cronJob struct {
		JobTemplate struct {
			Spec struct {
				Template podTemplate `yaml:"template"`
			} `yaml:"spec"`
		} `yaml:"jobTemplate"`
	}

	switch m.Kind {
	case "Pod":
		err := decodeSpec(m, &spec)
		return spec, err == nil, err
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		err := decodeSpec(m, &template)
		return template.Template.Spec, err == nil, err
	case "CronJob":
		err := decodeSpec(m, &cronJob)
		return cronJob.JobTemplate.Spec.Template.Spec, err == nil, err
	}
	return spec, false, nil
}

func decodeSpec(m manifest, v any) error {
	if err := m.Spec.Decode(v); err != nil {
		return fmt.Errorf("decoding spec: %w", err)
	}
	return nil
}

// pythonMount is what a container sees of the Python ConfigMaps it mounts.
type pythonMount struct {
	// configMaps are the Python ConfigMaps' names.
	configMaps []string
	// files are the visible keys and their contents.
	files map[string]string
}

// containerProblems checks one container against the Python ConfigMaps it
// mounts. A container that mounts none has no problems.
func containerProblems(c container, volumes []volume, configMaps map[string]map[string]string) []string {
	m := mountedPython(c, volumes, configMaps)
	if len(m.configMaps) == 0 {
		return nil
	}

	var problems []string
	for _, f := range pythonProjectFiles {
		if _, ok := m.files[f]; !ok {
			problems = append(problems, fmt.Sprintf("%s is not mounted from ConfigMap %s", f, strings.Join(m.configMaps, ", ")))
		}
	}
	problems = append(problems, imageProblems(c.Image, m.files)...)
	return append(problems, commandProblems(c)...)
}

// mountedPython collects the Python ConfigMaps a container mounts and the files
// it can see from them.
func mountedPython(c container, volumes []volume, configMaps map[string]map[string]string) pythonMount {
	byName := map[string]volume{}
	for _, v := range volumes {
		byName[v.Name] = v
	}

	m := pythonMount{files: map[string]string{}}
	for _, vm := range c.VolumeMounts {
		for _, src := range configMapSources(byName[vm.Name]) {
			cm := configMaps[src.Name]
			if !hasPython(cm) {
				continue
			}
			m.configMaps = append(m.configMaps, src.Name)
			for _, key := range visibleKeys(cm, src.Items, vm.SubPath) {
				m.files[key] = cm[key]
			}
		}
	}
	return m
}

// imageProblems checks that the image is a uv image on .python-version's Python.
func imageProblems(image string, files map[string]string) []string {
	match := uvImage.FindStringSubmatch(image)
	if match == nil {
		return []string{fmt.Sprintf("image %q is not ghcr.io/astral-sh/uv:pythonX.Y-…", image)}
	}
	version, ok := files[".python-version"]
	if !ok {
		return nil
	}
	if want := majorMinor(version); want != match[1] {
		return []string{fmt.Sprintf(".python-version is %s but the image's Python is %s", want, match[1])}
	}
	return nil
}

// commandProblems checks that the container starts with every uvRunFlags flag.
func commandProblems(c container) []string {
	cmd := strings.Join(append(append([]string{}, c.Command...), c.Args...), " ")
	var problems []string
	for _, flag := range uvRunFlags {
		if !strings.Contains(cmd, flag) {
			problems = append(problems, fmt.Sprintf("command does not use %q", flag))
		}
	}
	return problems
}

func configMapSources(v volume) []configMapSource {
	var sources []configMapSource
	if v.ConfigMap != nil {
		sources = append(sources, *v.ConfigMap)
	}
	if v.Projected != nil {
		for _, s := range v.Projected.Sources {
			if s.ConfigMap != nil {
				sources = append(sources, *s.ConfigMap)
			}
		}
	}
	return sources
}

func hasPython(data map[string]string) bool {
	for key := range data {
		if strings.HasSuffix(key, ".py") {
			return true
		}
	}
	return false
}

// visibleKeys returns the ConfigMap keys a mount exposes, given the volume's
// items (key → path; all keys at their own name when empty) and the mount's
// subPath (a single file, or a directory of them).
func visibleKeys(data map[string]string, items []keyToPath, subPath string) []string {
	paths := map[string]string{}
	if len(items) == 0 {
		for key := range data {
			paths[key] = key
		}
	} else {
		for _, it := range items {
			paths[path.Clean(it.Path)] = it.Key
		}
	}

	var keys []string
	sub := path.Clean(subPath)
	for p, key := range paths {
		if subPath == "" || p == sub || strings.HasPrefix(p, sub+"/") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// majorMinor reduces a .python-version ("3.15", "3.15.0\n") to "3.15".
func majorMinor(version string) string {
	parts := strings.SplitN(strings.TrimSpace(version), ".", 3)
	if len(parts) < 2 {
		return strings.TrimSpace(version)
	}
	return parts[0] + "." + parts[1]
}
