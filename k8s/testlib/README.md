# Integration test library

What the releases' Kubernetes integration tests (`k8s/<tier>/<release>/tests/`)
have in common, kept in one place so the suites cannot drift apart.

| File | Contents |
|---|---|
| `homelab_testlib/fixtures.py` | A pytest plugin: the API clients bound to `KUBECONFIG` |
| `homelab_testlib/namespaces.py` | `temporary_namespace`, which the suites build their namespace fixtures on |
| `homelab_testlib/certificates.py` | Requesting a certificate from cert-manager and waiting for it, with bounded polling |

Nothing is installed from here. A test project puts this directory on its
import path with `pythonpath = [".", "../../../testlib"]` in its
`pyproject.toml`, loads the fixtures with
`pytest_plugins = ["homelab_testlib.fixtures"]` in its `conftest.py`, and
declares for itself the dependencies of whatever it imports.
