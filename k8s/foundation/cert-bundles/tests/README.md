# Cert-bundles integration tests

A black-box test for the deployed CA bundles: that a new namespace receives
`cluster-ca-bundle`, and that a certificate from the internal issuer verifies
against it. It knows nothing about how the bundles were deployed — the cluster
is whichever one `KUBECONFIG` names.

| File | Contents |
|---|---|
| `conftest.py` | The fixtures shared through `k8s/testlib/`, and the throwaway namespace each test gets |
| `test_bundles.py` | Bounded polling, and the test |

## Running it

Against a cluster that is running cert-bundles, and that you are content to
have namespaces created and deleted in:

```bash
KUBECONFIG=/path/to/kubeconfig uv run pytest -m kubernetes -v
```

The test carries `@pytest.mark.kubernetes`, which `pyproject.toml` deselects by
default, so a bare `pytest` does not run it.
