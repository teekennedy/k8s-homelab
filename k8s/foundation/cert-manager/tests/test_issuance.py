"""Black-box tests for cert-manager issuing a certificate."""

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


def dns_names(certificate: x509.Certificate) -> list[str]:
    """The DNS names in a certificate's subject alternative name extension."""
    names = certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    return names.value.get_values_for_type(x509.DNSName)


@pytest.mark.kubernetes
def test_self_signed_issuer_issues_a_certificate(
    core_v1: client.CoreV1Api,
    custom_objects: client.CustomObjectsApi,
    namespace: str,
) -> None:
    # The test brings its own Issuer, so it depends on cert-manager and on
    # nothing that has been configured on top of it.
    issuer = "self-signed"
    custom_objects.create_namespaced_custom_object(
        CERT_MANAGER_GROUP,
        CERT_MANAGER_VERSION,
        namespace,
        "issuers",
        {
            "apiVersion": f"{CERT_MANAGER_GROUP}/{CERT_MANAGER_VERSION}",
            "kind": "Issuer",
            "metadata": {"name": issuer},
            "spec": {"selfSigned": {}},
        },
    )
    name = "leaf"
    dns_name = f"leaf.{namespace}.svc"
    request_certificate(
        custom_objects, namespace, name, dns_name, {"kind": "Issuer", "name": issuer}
    )

    leaf = await_certificate(
        core_v1,
        namespace,
        name,
        f"cert-manager to issue {namespace}/{name} from its own Issuer",
    )

    assert dns_names(leaf) == [dns_name]
    # Self-signed: the certificate's own key signed it.
    leaf.verify_directly_issued_by(leaf)
