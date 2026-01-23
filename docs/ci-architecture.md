# CI Architecture: Breaking the Circular Dependency

## The Problem

We had a circular dependency:
1. **Dagger** needs a container with all tools → uses **devenv** shell
2. **devenv** shell includes **lab** CLI for convenience
3. **lab** needs to be built from its Nix flake
4. If **lab's flake.nix is broken**, you can't build lab
5. If you can't build lab, devenv fails
6. If devenv fails, Dagger can't run
7. If Dagger can't run, you can't use CI to fix lab

## The Solution: Separate CI Container

We break the circular dependency by creating **two separate environments**:

### 1. Development Shell (includes lab)
```nix
# devenv.nix
packages = [
  lab  # Pre-built for developer convenience
  dagger
  kubectl
  # ... all other tools
];
```

**Purpose**: Fast, convenient development environment with lab pre-installed.

### 2. CI Container (excludes lab)
```nix
# devenv.nix
containers.ci = {
  name = "homelab-ci";
  copyToRoot = pkgs.buildEnv {
    paths = [
      dagger
      go
      nix
      kubectl
      # ... all other tools
      # NOTE: lab is NOT included here!
    ];
  };
};
```

**Purpose**: Minimal CI environment that can build lab from scratch.

## How It Works

### Development Workflow
```bash
# Developer enters devenv shell
devenv shell

# lab is already available
lab env list
lab k8s diff some-app
lab host deploy borg-0
```

### CI Workflow
```bash
# CI runs in containers.ci environment (no lab pre-installed)
# Dagger pipeline builds lab as first step

# Option 1: Direct dagger call
dagger call build --source=.

# Option 2: Using lab ci (if lab is already working)
lab ci build
lab ci lint
lab ci test
lab ci all
```

### Fixing a Broken Lab Build
```bash
# If lab's flake.nix breaks (e.g., wrong vendorHash)

# Option 1: Fix manually
cd cmd/lab
# Edit flake.nix to set vendorHash = pkgs.lib.fakeHash
nix build
# Get correct hash from error, update flake.nix
nix build  # Should succeed now

# Option 2: Use Dagger directly (doesn't need lab)
dagger call build-cli --source=.
# Dagger builds lab using Nix in a clean container

# Option 3: Update vendorHash automatically (future enhancement)
dagger call fix-lab-vendor-hash --source=.
```

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│ Development Environment (devenv shell)                      │
│                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐   │
│  │   lab    │  │  dagger  │  │ kubectl  │  │   ...    │   │
│  │ (pre-    │  │          │  │          │  │          │   │
│  │  built)  │  │          │  │          │  │          │   │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ CI Container (containers.ci)                                │
│                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                  │
│  │  dagger  │  │   nix    │  │ kubectl  │  ┌──────────┐   │
│  │          │  │          │  │          │  │   ...    │   │
│  │          │  │          │  │          │  │          │   │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘   │
│                                                              │
│  NO lab pre-installed!                                      │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ Dagger Pipeline                                       │  │
│  │                                                        │  │
│  │  Step 1: Build lab from source (nix build ./cmd/lab) │  │
│  │  Step 2: Run linters                                  │  │
│  │  Step 3: Run tests                                    │  │
│  │  Step 4: Build everything else                        │  │
│  │                                                        │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

## Command Reference

### lab ci Commands
```bash
# Run full CI pipeline
lab ci all

# Individual stages
lab ci lint       # Run all linters
lab ci validate   # Validate all configs
lab ci build      # Build all components
lab ci test       # Run all tests

# Build specific components
lab ci build lab         # Build just lab CLI
lab ci build nix         # Build NixOS configs
lab ci build --changed   # Build only changed components
```

### Dagger Functions
```bash
# Build lab using Nix (production build)
dagger call build-cli --source=.

# Build lab using Go (faster, for testing)
dagger call build-cli-go --source=.

# Get built binary
dagger call cli-nix --source=. export --path=./lab

# Full pipeline
dagger call all --source=.

# Individual stages
dagger call lint --source=.
dagger call validate --source=.
dagger call build --source=.
dagger call test --source=.
```

## Benefits

1. **No Circular Dependency**: CI can build lab even if lab is broken
2. **Clean Separation**: Dev environment vs CI environment
3. **Production Parity**: CI builds lab with Nix, just like production
4. **Developer Convenience**: Dev shell still has lab pre-installed
5. **Self-Healing**: CI can fix and rebuild lab automatically

## Future Enhancements

### Auto-fix vendorHash
Add a Dagger function that automatically updates vendorHash:

```go
// FixLabVendorHash builds lab, extracts the correct vendorHash, and updates flake.nix
func (m *Homelab) FixLabVendorHash(ctx context.Context, source *dagger.Directory) (*dagger.Directory, error) {
    // Try to build with fake hash
    // Parse error to get correct hash
    // Update flake.nix
    // Return updated source directory
}
```

Usage:
```bash
dagger call fix-lab-vendor-hash --source=. export --path=.
```

### CI Container Entry
Make `lab ci` commands actually enter the CI container:

```go
func runDaggerInCI(args ...string) error {
    if os.Getenv("HOMELAB_CI_CONTAINER") == "true" {
        return runDagger(args...)
    }

    // Enter CI container
    cmd := exec.Command("devenv", "container", "run", "ci", "--", "dagger", args...)
    return cmd.Run()
}
```

## Related Files

- `devenv.nix` - Defines both dev shell and CI container
- `cmd/lab/cmd/ci.go` - lab CI command implementations
- `.dagger/main.go` - Dagger pipeline definitions
- `cmd/lab/flake.nix` - Lab's Nix build configuration
