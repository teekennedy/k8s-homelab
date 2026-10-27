"""Black-box tests for the cluster's issuers."""

from typing import Any

import pytest
from homelab_testlib.certificates import (
    CERT_MANAGER_GROUP,
    CERT_MANAGER_VERSION,
    await_certificate,
    await_result,
    dns_names,
    request_certificate,
)
from kubernetes import client

# The issuer for certificates only in-cluster clients have to trust, and the one
# for certificates that outside clients do.
INTERNAL_ISSUER = "internal-ca"
PUBLIC_ISSUER = "letsencrypt-prod"


def is_ready(resource: dict[str, Any]) -> bool:
    """Whether a cert-manager resource reports the Ready condition as True."""
    conditions = resource.get("status", {}).get("conditions", [])
    return any(c["type"] == "Ready" and c["status"] == "True" for c in conditions)


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
def test_public_issuer_requests_shortlived_profile(
    custom_objects: client.CustomObjectsApi,
) -> None:
    issuer = custom_objects.get_cluster_custom_object(
        CERT_MANAGER_GROUP, CERT_MANAGER_VERSION, "clusterissuers", PUBLIC_ISSUER
    )
    acme = issuer["spec"].get("acme")
    if acme is None:
        pytest.skip(f"{PUBLIC_ISSUER} is backed by the internal CA, not ACME")

    # The expiry alerts and dashboard are tuned for 160h certificates.
    assert acme.get("profile") == "shortlived"


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
