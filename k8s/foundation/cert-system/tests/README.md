# Cert-system integration tests

Black-box tests for the deployed issuers: that both ClusterIssuers are ready,
and that the internal one issues a certificate. They know nothing about how the
issuers were deployed — the cluster is whichever one `KUBECONFIG` names.

Nothing is requested from the public issuer, only its readiness is checked, so
the suite costs nothing against an ACME server.

| File | Contents |
|---|---|
| `conftest.py` | Loading `KUBECONFIG`, and the throwaway namespace each test gets |
| `test_issuers.py` | Bounded polling, and the tests |

## Running them

Against a cluster that is running cert-system, and that you are content to have
namespaces created and deleted in:

```bash
KUBECONFIG=/path/to/kubeconfig uv run pytest -m kubernetes -v
```

Every test carries `@pytest.mark.kubernetes`, which `pyproject.toml` deselects
by default, so a bare `pytest` runs none of them.
