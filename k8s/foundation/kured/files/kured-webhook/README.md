# kured-webhook

Small HTTP webhook used by kured to create Alertmanager silences and emit a
KuredNodeRebooting alert when a node is drained/uncordoned.

## Running tests

```sh
cd k8s/foundation/kured/files/kured-webhook
uv sync --dev
uv run pytest
```

`uv sync` will create a local virtual environment in `.venv` for the tests.
