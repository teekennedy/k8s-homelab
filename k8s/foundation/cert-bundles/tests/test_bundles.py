"""Black-box tests for the CA bundle copied into every namespace."""

import pytest
from cryptography import x509
from homelab_testlib.certificates import (
    await_certificate,
    await_result,
    request_certificate,
)
from kubernetes import client

INTERNAL_ISSUER = "internal-ca"
# trust-manager copies the internal CA into every namespace under this name.
CA_BUNDLE_CONFIG_MAP = "cluster-ca-bundle"
CA_BUNDLE_KEY = "root-certs.pem"


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
