"""The throwaway namespaces the suites work in."""

import uuid
from collections.abc import Iterator
from contextlib import contextmanager

from kubernetes import client
from kubernetes.client.rest import ApiException


@contextmanager
def temporary_namespace(
    core_v1: client.CoreV1Api, prefix: str, managed_by: str
) -> Iterator[str]:
    """Create a namespace, and delete it on the way out.

    The name carries a random suffix so concurrent runs — or a previous run
    whose namespace is still terminating — cannot collide.
    """
    name = f"{prefix}-{uuid.uuid4().hex[:8]}"
    core_v1.create_namespace(
        client.V1Namespace(
            metadata=client.V1ObjectMeta(
                name=name,
                labels={"app.kubernetes.io/managed-by": managed_by},
            )
        )
    )
    try:
        yield name
    finally:
        # Deletion is asynchronous; the namespace is left Terminating rather
        # than waited on, because every run uses a fresh name.
        try:
            core_v1.delete_namespace(name)
        except ApiException as exc:
            if exc.status != 404:
                raise
