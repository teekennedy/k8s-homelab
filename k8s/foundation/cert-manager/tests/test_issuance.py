"""Black-box tests for cert-manager issuing a certificate."""

import pytest
from homelab_testlib.certificates import (
    CERT_MANAGER_GROUP,
    CERT_MANAGER_VERSION,
    await_certificate,
    dns_names,
    request_certificate,
)
from kubernetes import client


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
