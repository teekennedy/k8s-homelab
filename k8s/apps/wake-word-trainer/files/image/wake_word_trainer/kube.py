"""Start a training Job from the nightly CronJob, as `kubectl create job --from` does."""

import json
import os
import ssl
import time
import urllib.request
from pathlib import Path

SA = Path("/var/run/secrets/kubernetes.io/serviceaccount")


def _request(method: str, path: str, body=None):
    host = os.environ["KUBERNETES_SERVICE_HOST"]
    port = os.environ.get("KUBERNETES_SERVICE_PORT", "443")
    ctx = ssl.create_default_context(cafile=str(SA / "ca.crt"))
    req = urllib.request.Request(
        f"https://{host}:{port}{path}",
        method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={
            "Authorization": f"Bearer {(SA / 'token').read_text().strip()}",
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
    )
    with urllib.request.urlopen(req, context=ctx, timeout=15) as resp:
        return json.loads(resp.read())


def job_from_cronjob(cronjob: dict, name: str, force_env: dict[str, str]) -> dict:
    """The Job a manual trigger creates: the CronJob's template, plus `force_env` on every container."""
    template = json.loads(json.dumps(cronjob["spec"]["jobTemplate"]))
    spec = template["spec"]
    pod = spec["template"]["spec"]
    for c in pod.get("initContainers", []) + pod.get("containers", []):
        env = [e for e in c.get("env", []) if e["name"] not in force_env]
        c["env"] = env + [{"name": k, "value": v} for k, v in force_env.items()]
    meta = template.get("metadata", {})
    return {
        "apiVersion": "batch/v1",
        "kind": "Job",
        "metadata": {
            "name": name,
            "namespace": cronjob["metadata"]["namespace"],
            "labels": meta.get("labels", {}),
            "annotations": {
                **meta.get("annotations", {}),
                "cronjob.kubernetes.io/instantiate": "manual",
            },
            "ownerReferences": [
                {
                    "apiVersion": "batch/v1",
                    "kind": "CronJob",
                    "name": cronjob["metadata"]["name"],
                    "uid": cronjob["metadata"]["uid"],
                    "controller": False,
                }
            ],
        },
        "spec": spec,
    }


def create_training_job(namespace: str, cronjob_name: str) -> str:
    cj = _request(
        "GET", f"/apis/batch/v1/namespaces/{namespace}/cronjobs/{cronjob_name}"
    )
    name = f"{cronjob_name}-manual-{time.strftime('%Y%m%d%H%M%S', time.gmtime())}"[:63]
    job = job_from_cronjob(cj, name, {"WWT_FORCE": "1"})
    _request("POST", f"/apis/batch/v1/namespaces/{namespace}/jobs", job)
    return name
