# Reflector integration tests

Black-box tests for the deployed reflector: that it copies an annotated
ConfigMap into another namespace, and that it propagates later edits to the
copy. They know nothing about how reflector was deployed, and nothing about
Dagger — the cluster is whichever one `KUBECONFIG` names.

| File | Contents |
|---|---|
| `conftest.py` | Loading `KUBECONFIG`, and the pair of throwaway namespaces each test gets |
| `test_reflection.py` | The reflector annotations, bounded polling, and the two tests |

## Running them

Normally through Dagger, which builds a throwaway k3s cluster, deploys reflector
onto it and runs these against it:

```bash
dagger call test-kubernetes-integration --releases=reflector
```

See "Kubernetes integration tests" in `.dagger/README.md` for prerequisites.

Against a cluster you already have — one that is running reflector, and that you
are content to have namespaces created and deleted in:

```bash
KUBECONFIG=/path/to/kubeconfig uv run pytest -m kubernetes -v
```

## They do not run by accident

Every test here carries `@pytest.mark.kubernetes`, and `pyproject.toml`
deselects that marker by default. A bare `pytest` — an editor, a shell, `dagger
check test-python` — collects both tests and runs neither:

```
collected 2 items / 2 deselected / 0 selected
```

That `addopts` line is the whole mechanism, and it has to live here: the Dagger
Python runner passes no `-m` at all, because a command-line one would replace
this rather than narrow it. It matters because a developer's shell very often has
`KUBECONFIG` pointing at production, and these tests write to the cluster.

Once they have been selected, though, not running is a failure: there is no
`skip` anywhere in here. A missing `KUBECONFIG`, an unreachable API server or a
rejected credential raises, because a green run that silently tested nothing is
worse than a red one.

## Conventions worth keeping

- **Bounded polling, no sleeps.** `await_data` polls until a deadline and fails
  with what it was waiting for — it *is* the assertion, so the tests don't restate
  it. A 404 counts as "not yet"; any other API error propagates, so broken RBAC
  fails fast instead of timing out.
- **Unique names.** Namespaces carry a random suffix, so concurrent runs — or a
  previous run's namespaces still terminating — cannot collide.
- **Teardown regardless.** The namespace fixture deletes in a `finally`, so a
  failed assertion still cleans up.
