# Alert and Dashboard Mixins

This directory contains jsonnet-based templates and code to produce Grafana Dashboards and AlertManager Alert resources for kubernetes.

The mixins are based off of the same sources that [kube-prometheus-stack] uses - customized for k3s.
K3s bundles separate kubernetes services into a single binary, which means the metrics for these different services can be scraped from a single endpoint:
- cadvisor
- kubeApiserver
- kubeControllerManager
- kubeProxy
- kubeScheduler
- kubelet

## Using

The customized mixins are pre-rendered and the output is committed under the templates/ directory.
They are ready to be applied through helm.

## Developing

Dependencies are managed through [jsonnet-bundler].
Install jsonnet and jsonnet-bundler and run `jb install` to download dependencies to the vendor/ folder.

To build, run `jsonnet main.jsonnet -J vendor -m templates`. This will render the templates to the templates folder.

To update dependencies, run `jb update`.

[kube-prometheus-stack]: https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack
[jsonnet-bundler]: https://github.com/jsonnet-bundler/jsonnet-bundler
