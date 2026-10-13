# Cert-manager integration tests

A black-box test for the deployed cert-manager: that it issues a certificate
from an Issuer. The test creates its own self-signed Issuer, so it depends on
nothing configured on top of cert-manager. It knows nothing about how
cert-manager was deployed — the cluster is whichever one `KUBECONFIG` names.

| File | Contents |
|---|---|
| `conftest.py` | Loading `KUBECONFIG`, and the throwaway namespace each test gets |
| `test_issuance.py` | Bounded polling, and the test |

## Running it

Against a cluster that is running cert-manager, and that you are content to
have namespaces created and deleted in:

```bash
KUBECONFIG=/path/to/kubeconfig uv run pytest -m kubernetes -v
```

The test carries `@pytest.mark.kubernetes`, which `pyproject.toml` deselects by
default, so a bare `pytest` does not run it.
