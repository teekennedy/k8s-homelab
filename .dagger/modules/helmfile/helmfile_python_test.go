package main

import (
	"strings"
	"testing"
)

const pythonConfigMap = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  main.py: print("hi")
  .python-version: "3.15"
  pyproject.toml: "[project]"
  uv.lock: "version = 1"
`

func deployment(image, command, volume string) string {
	return `
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
        - name: app
          image: ` + image + `
          command: ["sh", "-c", ` + `"` + command + `"]
          volumeMounts:
            - name: app
              mountPath: /app
      volumes:
        - name: app
` + volume
}

const (
	goodImage   = "ghcr.io/astral-sh/uv:python3.15-alpine@sha256:abc"
	goodCommand = "uv run --frozen --no-dev --no-python-downloads python main.py"
	wholeVolume = "          configMap:\n            name: app\n"
)

func TestPythonRuntimeProblems(t *testing.T) {
	tests := []struct {
		name      string
		manifests string
		want      []string
	}{
		{
			name:      "uv image, project files and flags",
			manifests: pythonConfigMap + deployment(goodImage, goodCommand, wholeVolume),
		},
		{
			name: "ConfigMap without Python is ignored",
			manifests: strings.Replace(pythonConfigMap, "main.py", "main.sh", 1) +
				deployment("busybox", "sh main.sh", wholeVolume),
		},
		{
			name:      "plain python image",
			manifests: pythonConfigMap + deployment("python:3.15-alpine", goodCommand, wholeVolume),
			want:      []string{`image "python:3.15-alpine" is not`},
		},
		{
			name:      "image Python differs from .python-version",
			manifests: pythonConfigMap + deployment("ghcr.io/astral-sh/uv:python3.14-alpine", goodCommand, wholeVolume),
			want:      []string{".python-version is 3.15 but the image's Python is 3.14"},
		},
		{
			name:      "command allows Python downloads",
			manifests: pythonConfigMap + deployment(goodImage, "uv run --frozen python main.py", wholeVolume),
			want:      []string{`command does not use "--no-python-downloads"`},
		},
		{
			name:      "command runs python directly",
			manifests: pythonConfigMap + deployment(goodImage, "python main.py", wholeVolume),
			want: []string{
				`command does not use "uv run"`,
				`command does not use "--no-python-downloads"`,
				`command does not use "--frozen"`,
			},
		},
		{
			name: "items leave out the project files",
			manifests: pythonConfigMap + deployment(goodImage, goodCommand,
				"          configMap:\n            name: app\n            items:\n              - key: main.py\n                path: main.py\n"),
			want: []string{
				".python-version is not mounted",
				"pyproject.toml is not mounted",
				"uv.lock is not mounted",
			},
		},
		{
			name: "ConfigMap is missing the lock",
			manifests: strings.Replace(pythonConfigMap, `  uv.lock: "version = 1"`, "", 1) +
				deployment(goodImage, goodCommand, wholeVolume),
			want: []string{"uv.lock is not mounted"},
		},
		{
			name: "projected ConfigMap counts",
			manifests: pythonConfigMap + deployment("python:3.15", goodCommand,
				"          projected:\n            sources:\n              - configMap:\n                  name: app\n"),
			want: []string{`image "python:3.15" is not`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pythonRuntimeProblems([]byte(tt.manifests))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d problems, want %d:\n%s", len(got), len(tt.want), strings.Join(got, "\n"))
			}
			for i, want := range tt.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("problem %d = %q, want it to contain %q", i, got[i], want)
				}
			}
		})
	}
}

func TestPythonRuntimeProblemsSubPathMount(t *testing.T) {
	manifests := pythonConfigMap + `
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: job
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: job
              image: ` + goodImage + `
              command: ["sh", "-c", "` + goodCommand + `"]
              volumeMounts:
                - name: app
                  mountPath: /app/main.py
                  subPath: main.py
          volumes:
            - name: app
              configMap:
                name: app
`
	got, err := pythonRuntimeProblems([]byte(manifests))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !strings.HasPrefix(got[0], "CronJob/job container job: .python-version is not mounted") {
		t.Fatalf("got %q", got)
	}
}

func TestMajorMinor(t *testing.T) {
	for in, want := range map[string]string{"3.15": "3.15", "3.15.0\n": "3.15", "3.14\n\n": "3.14"} {
		if got := majorMinor(in); got != want {
			t.Errorf("majorMinor(%q) = %q, want %q", in, got, want)
		}
	}
}
