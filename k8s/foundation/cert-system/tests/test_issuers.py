"""Black-box tests for the cluster's issuers."""

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

# The issuer for certificates only in-cluster clients have to trust, and the one
# for certificates that outside clients do.
INTERNAL_ISSUER = "internal-ca"
PUBLIC_ISSUER = "letsencrypt-prod"

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


def is_ready(resource: dict[str, Any]) -> bool:
    """Whether a cert-manager resource reports the Ready condition as True."""
    conditions = resource.get("status", {}).get("conditions", [])
    return any(c["type"] == "Ready" and c["status"] == "True" for c in conditions)


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


def dns_names(certificate: x509.Certificate) -> list[str]:
    """The DNS names in a certificate's subject alternative name extension."""
    names = certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    return names.value.get_values_for_type(x509.DNSName)


@pytest.mark.kubernetes
@pytest.mark.parametrize("issuer", [INTERNAL_ISSUER, PUBLIC_ISSUER])
def test_cluster_issuer_is_ready(
    custom_objects: client.CustomObjectsApi, issuer: str
) -> None:
    def ready_issuer() -> dict[str, Any] | None:
        resource = custom_objects.get_cluster_custom_object(
            CERT_MANAGER_GROUP, CERT_MANAGER_VERSION, "clusterissuers", issuer
        )
        return resource if is_ready(resource) else None

    await_result(ready_issuer, f"ClusterIssuer {issuer} to become Ready")


@pytest.mark.kubernetes
def test_internal_issuer_issues_a_certificate(
    core_v1: client.CoreV1Api,
    custom_objects: client.CustomObjectsApi,
    namespace: str,
) -> None:
    name = "leaf"
    dns_name = f"leaf.{namespace}.svc"
    request_certificate(
        custom_objects,
        namespace,
        name,
        dns_name,
        {"kind": "ClusterIssuer", "name": INTERNAL_ISSUER},
    )

    leaf = await_certificate(
        core_v1,
        namespace,
        name,
        f"cert-manager to issue {namespace}/{name} from {INTERNAL_ISSUER}",
    )

    assert dns_names(leaf) == [dns_name]
    # Signed by the CA rather than by its own key.
    assert leaf.issuer != leaf.subject
