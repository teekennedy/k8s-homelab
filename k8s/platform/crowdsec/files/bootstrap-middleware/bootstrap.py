#!/usr/bin/env python3
"""
CrowdSec Bouncer Middleware Bootstrap Script

This script automates the post-deployment setup of the CrowdSec bouncer middleware:
1. Checks if bouncer API key is already configured
2. Generates a new key if needed
3. Updates the Traefik middleware
4. Restarts Traefik to apply changes
"""

import sys
import time

from kubernetes import client, config, stream
from kubernetes.client.rest import ApiException

CROWDSEC_NAMESPACE = "crowdsec"
TRAEFIK_NAMESPACE = "kube-system"
MIDDLEWARE_NAME = "crowdsec-bouncer"
TRAEFIK_DEPLOYMENT = "traefik"
LAPI_DEPLOYMENT = "crowdsec-lapi"
BOUNCER_NAME = "traefik-bouncer"
PLACEHOLDER_KEY = "REPLACE_WITH_REAL_KEY"


def load_kubernetes_config():
    """Load Kubernetes configuration (in-cluster or kubeconfig)."""
    try:
        config.load_incluster_config()
        print("Loaded in-cluster Kubernetes configuration")
    except config.ConfigException:
        config.load_kube_config()
        print("Loaded kubeconfig configuration")


def check_middleware_configured(custom_api):
    """
    Check if the middleware already has a valid bouncer key.

    Returns:
        bool: True if already configured, False otherwise
    """
    try:
        middleware = custom_api.get_namespaced_custom_object(
            group="traefik.io",
            version="v1alpha1",
            namespace=CROWDSEC_NAMESPACE,
            plural="middlewares",
            name=MIDDLEWARE_NAME,
        )

        # Check if the key is set and not the placeholder
        current_key = (
            middleware.get("spec", {})
            .get("plugin", {})
            .get("crowdsec-bouncer", {})
            .get("CrowdsecLapiKey", "")
        )

        if current_key and current_key != PLACEHOLDER_KEY:
            print(f"Middleware already configured with a valid bouncer key")
            return True

        print(f"Middleware has placeholder key, needs configuration")
        return False

    except ApiException as e:
        if e.status == 404:
            print(
                f"Middleware {MIDDLEWARE_NAME} not found in namespace {CROWDSEC_NAMESPACE}"
            )
            sys.exit(1)
        raise


def get_lapi_pod(core_api):
    """
    Get the first running pod from the crowdsec-lapi deployment.

    Returns:
        str: Pod name
    """
    try:
        pods = core_api.list_namespaced_pod(
            namespace=CROWDSEC_NAMESPACE,
            label_selector=f"k8s-app=crowdsec,type=lapi",
        )

        if not pods.items:
            print(f"No pods found for deployment {LAPI_DEPLOYMENT}")
            sys.exit(1)

        # Find first running pod
        for pod in pods.items:
            if pod.status.phase == "Running":
                print(f"Found LAPI pod: {pod.metadata.name}")
                return pod.metadata.name

        print(f"No running pods found for deployment {LAPI_DEPLOYMENT}")
        sys.exit(1)

    except ApiException as e:
        print(f"Error listing pods: {e}")
        sys.exit(1)


def generate_bouncer_key(core_api, pod_name):
    """
    Generate a new bouncer API key by execing into the LAPI pod.

    Args:
        pod_name: Name of the LAPI pod

    Returns:
        str: Generated API key
    """
    try:
        print(f"Generating bouncer key in pod {pod_name}...")

        exec_command = [
            "cscli",
            "bouncers",
            "add",
            BOUNCER_NAME,
            "-o",
            "raw",
        ]

        resp = stream.stream(
            core_api.connect_get_namespaced_pod_exec,
            pod_name,
            CROWDSEC_NAMESPACE,
            command=exec_command,
            stderr=True,
            stdin=False,
            stdout=True,
            tty=False,
        )

        api_key = resp.strip()

        if not api_key:
            print("Failed to generate bouncer key (empty response)")
            sys.exit(1)

        print(f"Successfully generated bouncer key")
        return api_key

    except ApiException as e:
        print(f"Error executing command in pod: {e}")
        sys.exit(1)


def update_middleware(custom_api, api_key):
    """
    Update the middleware with the new bouncer API key.

    Args:
        api_key: The bouncer API key to set
    """
    try:
        print(f"Updating middleware {MIDDLEWARE_NAME}...")

        # Patch the middleware with the new key
        patch = {"spec": {"plugin": {"crowdsec-bouncer": {"CrowdsecLapiKey": api_key}}}}

        custom_api.patch_namespaced_custom_object(
            group="traefik.io",
            version="v1alpha1",
            namespace=CROWDSEC_NAMESPACE,
            plural="middlewares",
            name=MIDDLEWARE_NAME,
            body=patch,
        )

        print(f"Successfully updated middleware")

    except ApiException as e:
        print(f"Error updating middleware: {e}")
        sys.exit(1)


def restart_traefik(apps_api):
    """
    Restart the Traefik deployment and wait for rollout to complete.
    """
    try:
        print(f"Restarting Traefik deployment...")

        # Trigger rollout restart by updating the restart annotation
        now = time.strftime("%Y%m%d%H%M%S")
        patch = {
            "spec": {
                "template": {
                    "metadata": {
                        "annotations": {"kubectl.kubernetes.io/restartedAt": now}
                    }
                }
            }
        }

        apps_api.patch_namespaced_deployment(
            name=TRAEFIK_DEPLOYMENT,
            namespace=TRAEFIK_NAMESPACE,
            body=patch,
        )

        print(f"Triggered Traefik restart, waiting for rollout...")

        # Wait for rollout to complete (simplified check)
        max_wait = 120  # 2 minutes
        wait_interval = 5
        elapsed = 0

        while elapsed < max_wait:
            deployment = apps_api.read_namespaced_deployment(
                name=TRAEFIK_DEPLOYMENT,
                namespace=TRAEFIK_NAMESPACE,
            )

            # Check if deployment is ready
            if (
                deployment.status.updated_replicas == deployment.spec.replicas
                and deployment.status.ready_replicas == deployment.spec.replicas
                and deployment.status.available_replicas == deployment.spec.replicas
            ):
                print(f"Traefik rollout completed successfully")
                return

            time.sleep(wait_interval)
            elapsed += wait_interval
            print(f"Waiting for rollout... ({elapsed}s/{max_wait}s)")

        print(f"Warning: Rollout did not complete within {max_wait}s")

    except ApiException as e:
        print(f"Error restarting Traefik: {e}")
        sys.exit(1)


def main():
    """Main execution flow."""
    print("CrowdSec Bouncer Middleware Bootstrap Starting...")

    # Load Kubernetes configuration
    load_kubernetes_config()

    # Initialize API clients
    core_api = client.CoreV1Api()
    apps_api = client.AppsV1Api()
    custom_api = client.CustomObjectsApi()

    # Check if middleware is already configured
    if check_middleware_configured(custom_api):
        print("Bootstrap completed (already configured)")
        sys.exit(0)

    # Get LAPI pod
    lapi_pod = get_lapi_pod(core_api)

    # Generate bouncer key
    api_key = generate_bouncer_key(core_api, lapi_pod)

    # Update middleware
    update_middleware(custom_api, api_key)

    # Restart Traefik
    restart_traefik(apps_api)

    print("Bootstrap completed successfully")
    sys.exit(0)


if __name__ == "__main__":
    main()
