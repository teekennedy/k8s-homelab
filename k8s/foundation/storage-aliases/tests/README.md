# Storage-aliases integration tests

Black-box tests for the aliased storage classes: that a claim against each one
is provisioned. They know nothing about how the classes were deployed — the
cluster is whichever one `KUBECONFIG` names.

| File | Contents |
|---|---|
| `conftest.py` | The fixtures shared through `k8s/testlib/`, and the throwaway namespace each test gets |
| `test_provisioning.py` | Bounded polling, and the test |

## Running them

Against a cluster that has the aliases, and that you are content to have
namespaces and volumes created and deleted in:

```bash
KUBECONFIG=/path/to/kubeconfig uv run pytest -m kubernetes -v
```

Every test carries `@pytest.mark.kubernetes`, which `pyproject.toml` deselects
by default, so a bare `pytest` runs none of them.
