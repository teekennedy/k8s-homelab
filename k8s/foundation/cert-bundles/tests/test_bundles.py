"""Black-box tests for the CA bundle copied into every namespace."""

import base64
import time
from collections.abc import Callable
from typing import Any

import pytest
from cryptography import x509
from kubernetes import client
from kubernetes.client.rest import ApiException

CERT_MANAGER_GROUP = "cert-manager.io"
CERT_MANAGER_VERSION = "v1"

INTERNAL_ISSUER = "internal-ca"
# trust-manager copies the internal CA into every namespace under this name.
CA_BUNDLE_CONFIG_MAP = "cluster-ca-bundle"
CA_BUNDLE_KEY = "root-certs.pem"

# Issuing from a CA is a signature, not a challenge, so it normally takes a few
# seconds. The workflow waits for the rollout before running these, so the
# ceiling only has to cover a slow reconcile, not a cold start.
TIMEOUT = 60.0
POLL_INTERVAL = 0.5


def await_result(fetch: Callable[[], Any], description: str) -> Any:
    """Poll fetch until it returns something other than None, or fail.

    This is the assertion, not a prelude to one: timing out reports what was
    being waited for. A 404 counts as "not yet" — the normal state of an object
    a controller has not created — but any other API error propagates, so a
    broken RBAC rule fails the test instead of burning the whole timeout.
    """
    deadline = time.monotonic() + TIMEOUT
    while True:
        try:
            result = fetch()
            if result is not None:
                return result
        except ApiException as exc:
            if exc.status != 404:
                raise
        if time.monotonic() >= deadline:
            raise AssertionError(
                f"timed out after {TIMEOUT:.0f}s waiting for {description}"
            )
        time.sleep(POLL_INTERVAL)


def request_certificate(
    custom_objects: client.CustomObjectsApi,
    namespace: str,
    name: str,
    dns_name: str,
    issuer: dict[str, str],
) -> None:
    """Ask cert-manager for a certificate for dns_name, stored in Secret name."""
    custom_objects.create_namespaced_custom_object(
        CERT_MANAGER_GROUP,
        CERT_MANAGER_VERSION,
        namespace,
        "certificates",
        {
            "apiVersion": f"{CERT_MANAGER_GROUP}/{CERT_MANAGER_VERSION}",
            "kind": "Certificate",
            "metadata": {"name": name},
            "spec": {
                "secretName": name,
                "dnsNames": [dns_name],
                "issuerRef": {"group": CERT_MANAGER_GROUP, **issuer},
            },
        },
    )


def await_certificate(
    core_v1: client.CoreV1Api, namespace: str, name: str, description: str
) -> x509.Certificate:
    """Wait for Secret name to hold an issued certificate, and return it."""

    def issued() -> x509.Certificate | None:
        secret = core_v1.read_namespaced_secret(name=name, namespace=namespace)
        pem = (secret.data or {}).get("tls.crt")
        return x509.load_pem_x509_certificate(base64.b64decode(pem)) if pem else None

    return await_result(issued, description)


@pytest.mark.kubernetes
def test_internal_certificate_verifies_against_the_namespace_bundle(
    core_v1: client.CoreV1Api,
    custom_objects: client.CustomObjectsApi,
    namespace: str,
) -> None:
    def bundled_roots() -> list[x509.Certificate] | None:
        bundle = core_v1.read_namespaced_config_map(
            name=CA_BUNDLE_CONFIG_MAP, namespace=namespace
        )
        pem = (bundle.data or {}).get(CA_BUNDLE_KEY)
        return x509.load_pem_x509_certificates(pem.encode()) if pem else None

    roots = await_result(
        bundled_roots,
        f"trust-manager to copy {CA_BUNDLE_CONFIG_MAP} into {namespace}",
    )

    name = "leaf"
    request_certificate(
        custom_objects,
        namespace,
        name,
        f"leaf.{namespace}.svc",
        {"kind": "ClusterIssuer", "name": INTERNAL_ISSUER},
    )
    leaf = await_certificate(
        core_v1,
        namespace,
        name,
        f"cert-manager to issue {namespace}/{name} from {INTERNAL_ISSUER}",
    )

    # What a workload mounting the bundle relies on: a certificate from the
    # internal issuer verifies against the roots in its own namespace.
    issuers = [root for root in roots if root.subject == leaf.issuer]
    assert issuers, f"no certificate in {CA_BUNDLE_CONFIG_MAP} is the leaf's issuer"
    leaf.verify_directly_issued_by(issuers[0])
