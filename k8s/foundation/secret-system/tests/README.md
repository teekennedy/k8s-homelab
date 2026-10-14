# Secret-system integration tests

Black-box tests for the deployed secret generator: that it fills in the keys an
annotated Secret asks for, leaves alone the ones it does not ask for, and
replaces a generated value when asked to regenerate it. They know nothing about
how the generator was deployed — the cluster is whichever one `KUBECONFIG`
names.

| File | Contents |
|---|---|
| `conftest.py` | The fixtures shared through `k8s/testlib/`, and the throwaway namespace each test gets |
| `test_generation.py` | The generator's annotations, bounded polling, and the three tests |

## Running them

Against a cluster that is running the secret generator, and that you are
content to have namespaces created and deleted in:

```bash
KUBECONFIG=/path/to/kubeconfig uv run pytest -m kubernetes -v
```

Every test carries `@pytest.mark.kubernetes`, which `pyproject.toml` deselects
by default, so a bare `pytest` runs none of them.
