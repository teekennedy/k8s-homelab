// The devenv shell as a container: the toolchain every other module runs its
// checks in, and the devenv images the release pipeline publishes.
//
// Every tool a check needs comes from devenv.lock, so a check runs the same
// alejandra, black or golangci-lint in `dagger check` as in a local shell.
package main

import (
	"sort"
	"strings"

	"dagger/devenv/internal/dagger"
)

// Container image constants with renovate annotations for automated updates.
const (
	// renovate: datasource=docker depName=ghcr.io/cachix/devenv/devenv
	devenvImage = "ghcr.io/cachix/devenv/devenv:v2.4.0"
)

const imageSource = "https://github.com/teekennedy/k8s-homelab"

// Devenv builds containers from the repo's devenv definition.
type Devenv struct {
	// Source holds the files devenv reads to build any profile: devenv.nix,
	// devenv.yaml, devenv.lock, and all of cmd/lab, which devenv.yaml imports
	// and whose CLI the interactive profile builds from source.
	// +private
	Source *dagger.Directory
	// ToolchainSource is Source without the lab CLI's own source. The ci and
	// integration profiles never build the CLI, so editing it must not
	// invalidate the container every check runs in.
	// +private
	ToolchainSource *dagger.Directory
}

// Builds the devenv shell: the toolchain container every check runs in, and the published devenv images.
func New(
	// +defaultPath="/"
	// +ignore=["*", "!devenv.nix", "!devenv.yaml", "!devenv.lock", "!cmd/lab/**", "cmd/lab/result", "cmd/lab/.direnv"]
	source *dagger.Directory,
	// Loaded separately rather than filtered out of source, so that it is
	// content-addressed from the client filesystem: a directory derived from
	// source would carry source's identity, Go files and all.
	// +defaultPath="/"
	// +ignore=["*", "!devenv.nix", "!devenv.yaml", "!devenv.lock", "!cmd/lab/devenv.nix"]
	toolchainSource *dagger.Directory,
) *Devenv {
	return &Devenv{Source: source, ToolchainSource: toolchainSource}
}

// Toolchain returns the devenv shell for profiles with the language toolchain
// caches attached: the container the other modules' checks run in.
//
// The "ci" profile holds exactly what `dagger check` needs. The Kubernetes
// integration workflow adds "integration", which brings kubectl and curl; no
// check runs with it, so `dagger check` keeps running in a container that
// cannot reach a cluster even if something tried.
//
// It is built without the lab CLI's source, so it cannot build the
// "interactive" profile, which installs the CLI; use Container for that.
func (m *Devenv) Toolchain(
	// devenv profiles to activate (https://devenv.sh/profiles/).
	// +optional
	// +default=["ci"]
	profiles []string,
) *dagger.Container {
	return withToolchainCaches(devenvShell(m.ToolchainSource, nil, profiles))
}

// IntegrationToolchain is Toolchain for the "ci" and "integration" profiles:
// the container the Kubernetes integration workflow runs in. It takes no
// arguments so that a dagger.toml setting can link to it.
func (m *Devenv) IntegrationToolchain() *dagger.Container {
	return m.Toolchain([]string{"ci", "integration"})
}

// Container builds the devenv nix environment as a minimal "from scratch"
// container, labelled for publishing. Uses `devenv container copy shell` to
// produce a minimal nix2container image containing only the runtime closure
// (no build dependencies). The nix store is cached across runs so packages are
// only built once.
func (m *Devenv) Container(
	// Optional host nix daemon socket for faster builds on NixOS CI hosts.
	// When provided, the host daemon's store is used as a substituter so
	// pre-built packages are fetched rather than rebuilt from source.
	// Note: only works when the host and container share the same platform
	// (e.g. Linux-to-Linux). Does not work with macOS host daemons due to
	// cross-platform path handling differences.
	// +optional
	nixDaemon *dagger.Socket,
	// devenv profiles to activate (https://devenv.sh/profiles/). Defaults to
	// the "ci" profile, which holds exactly the toolchain the checks need. The
	// "interactive" profile — cluster tooling, the kubeconfig enterShell, the
	// dagger CLI, the git hook — is deliberately not built here; pass it
	// explicitly if you want a full shell to poke at.
	// +optional
	// +default=["ci"]
	profiles []string,
) *dagger.Container {
	description := "Devenv shell profiles: " + strings.Join(profiles, ", ")
	if len(profiles) == 1 && profiles[0] == "ci" {
		description = "Devenv shell CI profile"
	} else if len(profiles) == 0 {
		description = "Devenv shell"
	}
	return withImageLabels(devenvShell(m.Source, nixDaemon, profiles), map[string]string{
		"org.opencontainers.image.title":         "Devenv shell",
		"org.opencontainers.image.description":   description,
		"org.opencontainers.image.source":        imageSource,
		"org.opencontainers.image.url":           imageSource,
		"org.opencontainers.image.documentation": imageSource,
		"org.opencontainers.image.licenses":      "MIT",
		"org.opencontainers.image.version":       "dev",
		"org.opencontainers.image.revision":      "unknown",
	})
}

// withImageLabels sets labels, and the OCI ones as manifest annotations too.
func withImageLabels(container *dagger.Container, labels map[string]string) *dagger.Container {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		container = container.WithLabel(key, labels[key])
		if strings.HasPrefix(key, "org.opencontainers.image.") {
			container = container.WithAnnotation(key, labels[key])
		}
	}
	return container
}

// devenvContainer returns a devenv container with a persistent nix store cache
// mounted, so nix/devenv operations only build or fetch what changed. The
// cache volume name includes the image tag so it auto-invalidates when the
// devenv image is updated (e.g. by Renovate).
func devenvContainer() *dagger.Container {
	nixCacheKey := "devenv-nix-" + devenvImage
	baseNix := dag.Container().From(devenvImage).WithUser("root").Directory("/nix")

	return dag.Container().
		From(devenvImage).
		WithUser("root").
		WithMountedCache("/nix", dag.CacheVolume(nixCacheKey), dagger.ContainerWithMountedCacheOpts{
			Source: baseNix,
		}).
		// Suppress zsh-specific setup (compdef errors) in container context
		WithEnvVariable("DEVENV_ZSH_DISABLE", "1")
}

// devenvShell builds the devenv shell for the given profiles as a minimal
// "from scratch" container.
//
// `devenv container copy shell` produces a nix2container image holding only the
// runtime closure. That image's config carries devenv's own `env.*` settings
// (SSL_CERT_FILE, the UV_PYTHON_* pins, DEVENV_PROFILE) but not PATH, so PATH
// is set here by expanding DEVENV_PROFILE, which contains exactly the packages
// the active profiles declare.
func devenvShell(
	source *dagger.Directory,
	nixDaemon *dagger.Socket,
	profiles []string,
) *dagger.Container {
	builder := devenvContainer().
		WithDirectory("/devenv", source).
		WithWorkdir("/devenv")

	devenvOpts := []string{"--no-tui", "--option", "devenv.warnOnNewVersion:bool", "false"}
	for _, p := range profiles {
		devenvOpts = append(devenvOpts, "--profile", p)
	}
	if nixDaemon != nil {
		builder = builder.
			WithUnixSocket("/nix/var/nix/daemon-socket/socket", nixDaemon)
		// Use the host daemon as a substituter. The "daemon" keyword connects
		// to the socket at the default path (/nix/var/nix/daemon-socket/socket).
		// This works on NixOS CI hosts where architecture matches the container.
		devenvOpts = append(
			devenvOpts,
			"--nix-option", "extra-substituters", "daemon",
			"--nix-option", "require-sigs", "false",
		)
	}

	// Build the minimal shell container and export as docker-archive tarball.
	// devenv container copy uses nix2container + skopeo under the hood.
	// The --registry flag accepts any skopeo transport, including docker-archive.
	// The container name ("shell") is appended to the registry path by devenv,
	// so "docker-archive:/tmp/" produces the file "/tmp/shell".
	tarball := builder.
		WithExec(append([]string{"devenv", "build", "shell"}, devenvOpts...)).
		WithExec(append([]string{"devenv", "container", "copy", "shell", "--registry", "docker-archive:/tmp/"}, devenvOpts...)).
		File("/tmp/shell")

	return dag.Container().Import(tarball).
		WithEnvVariable("PATH", "$DEVENV_PROFILE/bin:/bin", dagger.ContainerWithEnvVariableOpts{Expand: true}).
		WithWorkdir("/src")
}

// withToolchainCaches attaches the caches every language toolchain in the ci
// profile wants.
//
// They are attached once here rather than at each call site so that a Go check
// and a Python check running in parallel share one cache volume each, and so
// adding a tool to the ci profile doesn't mean remembering to wire up its cache
// separately.
//
// Every path is named by the tool's own environment variable rather than left
// to default under $HOME. The devenv image sets HOME=/env and User=user, and
// hanging caches off that would couple the mount layout to devenv's container
// internals; the execs run as root so the mounts are writable.
func withToolchainCaches(c *dagger.Container) *dagger.Container {
	const (
		goModCache     = "/cache/go/mod"
		goBuildCache   = "/cache/go/build"
		uvCache        = "/cache/uv"
		helmCacheHome  = "/cache/helm"
		helmConfigHome = "/config/helm"
		helmDataHome   = "/data/helm"
		helmfileCache  = "/cache/helmfile"
	)

	return c.
		WithUser("root").
		// Create statically linked go binaries
		WithEnvVariable("CGO_ENABLED", "0").
		// Do not stamp go binaries with version control information
		WithEnvVariable("GOFLAGS", "-buildvcs=false").
		WithEnvVariable("GOMODCACHE", goModCache).
		WithMountedCache(goModCache, dag.CacheVolume("homelab-go-mod")).
		WithEnvVariable("GOCACHE", goBuildCache).
		WithMountedCache(goBuildCache, dag.CacheVolume("homelab-go-build")).
		WithEnvVariable("UV_CACHE_DIR", uvCache).
		WithMountedCache(uvCache, dag.CacheVolume("homelab-uv")).
		WithEnvVariable("HELM_CACHE_HOME", helmCacheHome).
		WithMountedCache(helmCacheHome, dag.CacheVolume("homelab-helm-cache")).
		WithEnvVariable("HELM_CONFIG_HOME", helmConfigHome).
		WithMountedCache(helmConfigHome, dag.CacheVolume("homelab-helm-config")).
		WithEnvVariable("HELM_DATA_HOME", helmDataHome).
		WithMountedCache(helmDataHome, dag.CacheVolume("homelab-helm-data")).
		WithEnvVariable("HELMFILE_CACHE_HOME", helmfileCache).
		WithMountedCache(helmfileCache, dag.CacheVolume("homelab-helmfile"))
}
