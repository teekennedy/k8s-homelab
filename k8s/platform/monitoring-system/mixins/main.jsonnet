# We use helper functions from kube-prometheus to generate dashboards and alerts for Kubernetes.
local addMixin = (import 'kube-prometheus/lib/mixin.libsonnet');

local kubernetesMixin = addMixin({
  name: 'kubernetes',
  namespace: 'monitoring-system',
  dashboardFolder: 'Kubernetes',
  mixin: (import 'kubernetes-mixin/mixin.libsonnet') + {
    _config+:: {
      cadvisorSelector: 'job="kubelet"',
      kubeletSelector: 'job="kubelet"',
      kubeSchedulerSelector: 'job="kubelet"',
      kubeControllerManagerSelector: 'job="kubelet"',
      kubeApiserverSelector: 'job="kubelet"',
      kubeProxySelector: 'job="kubelet"',
      grafanaK8s+: {
        grafanaTimezone: 'browser',
      },
    },
    # KubeJobFailed blocks kured, and nixos-rollout waits on kured: a failed
    # rollout would hold off the reboots the next rollout needs. A stalled
    # rollout is reported by NixosRebootPendingTooLong instead.
    prometheusAlerts+:: {
      groups: [
        g {
          rules: [
            if std.objectHas(r, 'alert') && r.alert == 'KubeJobFailed' then
              r { expr: std.strReplace(r.expr, 'job="kube-state-metrics"', 'job="kube-state-metrics", job_name!~"nixos-rollout-.*"') }
            else r
            for r in g.rules
          ],
        }
        for g in super.groups
      ],
    },
  },
});

local nodeExporterMixin = addMixin({
  name: 'node-exporter',
  namespace: 'monitoring-system',
  dashboardFolder: 'General',
  mixin: (import 'node-mixin/mixin.libsonnet') + {
    _config+:: {
      grafanaK8s+: {
        grafanaTimezone: 'browser',
      },
      nodeExporterSelector: 'job="node-exporter"',
    },
  },
});

local corednsMixin = addMixin({
  name: 'coredns',
  namespace: 'monitoring-system',
  dashboardFolder: 'DNS',
  mixin: (import 'coredns-mixin/mixin.libsonnet') + {
    _config+:: {
      corednsSelector: 'job="coredns"',
    },
  },
});

local grafanaMixin = addMixin({
  name: 'grafana',
  namespace: 'monitoring-system',
  dashboardFolder: 'Grafana',
  mixin: (import 'grafana-mixin/mixin.libsonnet') + {
    _config+:: {},
  },
});

local prometheusMixin = addMixin({
  name: 'prometheus',
  namespace: 'monitoring-system',
  dashboardFolder: 'Prometheus',
  mixin: (import 'prometheus/mixin.libsonnet') + {
    _config+:: {
      prometheusSelector: 'job="monitoring-system-kube-pro-prometheus"',
      showMultiCluster: false,
      grafanaPrometheus+: {
        # Sadly there is no timezone override for prometheus-mixin right now
        # timezone: 'browser',
      },
    },
  },
});

local prometheusOperatorMixin = addMixin({
  name: 'prometheus-operator',
  namespace: 'monitoring-system',
  dashboardFolder: 'Prometheus Operator',
  mixin: (import 'prometheus-operator-mixin/mixin.libsonnet') + {
    _config+:: {
      prometheusOperatorSelector: 'job="monitoring-system-kube-pro-operator"',
    },
  },
});

local alertmanagerMixin = addMixin({
  name: 'alertmanager',
  namespace: 'monitoring-system',
  dashboardFolder: 'Alertmanager',
  mixin: (import 'alertmanager/mixin.libsonnet') + {
    _config+:: {
      alertmanagerSelector: 'job="monitoring-system-kube-pro-alertmanager"',
      alertmanagerClusterLabels: 'job,namespace',
      alertmanagerNameLabels: 'namespace,pod',
    },
  },
});

# Watchdog from kube-prometheus. InfoInhibitor is left out: it only makes sense
# with a matching Alertmanager inhibit rule and null route. TargetDown is left
# out because PrometheusTargetScrapeFailed (files/scrape-health-rules.json)
# already alerts per target, including in jobs too large to cross its 10%.
local generalMixin = addMixin({
  name: 'general',
  namespace: 'monitoring-system',
  mixin: (import 'kube-prometheus/components/mixin/alerts/general.libsonnet') + {
    prometheusAlerts+:: {
      groups: [
        g { rules: [r for r in g.rules if !std.member(['InfoInhibitor', 'TargetDown'], r.alert)] }
        for g in super.groups
      ],
    },
  },
});

local stripJsonExtension(name) =
  local extensionIndex = std.findSubstr('.json', name);
  local n = if std.length(extensionIndex) < 1 then name else std.substr(name, 0, extensionIndex[0]);
  n;

local grafanaDashboardConfigMap(folder, name, json) = {
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name: 'grafana-dashboard-%s' % stripJsonExtension(name),
    namespace: 'monitoring-system',
    labels: {
      grafana_dashboard: '1',
    },
  },
  data: {
    [name]: std.manifestJsonEx(json, ''),
  },
};

local excludedDashboards = [
  'k8s-resources-windows-cluster.json',
  'k8s-resources-windows-namespace.json',
  'k8s-resources-windows-pod.json',
  'k8s-windows-cluster-rsrc-use.json',
  'k8s-windows-node-rsrc-use.json',
  'nodes-aix.json',
  'nodes-darwin.json',
  'prometheus-remote-write.json',
];

local generateGrafanaDashboardConfigMaps(mixin) = if std.objectHas(mixin, 'grafanaDashboards') && mixin.grafanaDashboards != null then {
  ['grafana-dashboard-' + name]: grafanaDashboardConfigMap(folder, name, mixin.grafanaDashboards[folder][name] + {timezone: "browser"})
  for folder in std.objectFields(mixin.grafanaDashboards)
  for name in std.objectFields(mixin.grafanaDashboards[folder])
  if std.member(excludedDashboards, name) == false
} else {};

local nodeExporterMixinHelmGrafanaDashboards = generateGrafanaDashboardConfigMaps(nodeExporterMixin);
local kubernetesMixinHelmGrafanaDashboards = generateGrafanaDashboardConfigMaps(kubernetesMixin);
local corednsMixinHelmGrafanaDashboards = generateGrafanaDashboardConfigMaps(corednsMixin);
local grafanaMixinHelmGrafanaDashboards = generateGrafanaDashboardConfigMaps(grafanaMixin);
local prometheusMixinHelmGrafanaDashboards = generateGrafanaDashboardConfigMaps(prometheusMixin);
local prometheusOperatorMixinHelmGrafanaDashboards = generateGrafanaDashboardConfigMaps(prometheusOperatorMixin);
local alertmanagerMixinHelmGrafanaDashboards = generateGrafanaDashboardConfigMaps(alertmanagerMixin);

local grafanaDashboards =
  kubernetesMixinHelmGrafanaDashboards +
  nodeExporterMixinHelmGrafanaDashboards +
  corednsMixinHelmGrafanaDashboards +
  grafanaMixinHelmGrafanaDashboards +
  prometheusMixinHelmGrafanaDashboards +
  prometheusOperatorMixinHelmGrafanaDashboards +
  alertmanagerMixinHelmGrafanaDashboards;


local prometheusAlerts = {
  'kubernetes-mixin-rules.json': kubernetesMixin.prometheusRules,
  'node-exporter-mixin-rules.json': nodeExporterMixin.prometheusRules,
  'coredns-mixin-rules.json': corednsMixin.prometheusRules,
  'grafana-mixin-rules.json': grafanaMixin.prometheusRules,
  'prometheus-mixin-rules.json': prometheusMixin.prometheusRules,
  'prometheus-operator-mixin-rules.json': prometheusOperatorMixin.prometheusRules,
  'alertmanager-mixin-rules.json': alertmanagerMixin.prometheusRules,
  'general-mixin-rules.json': generalMixin.prometheusRules,
};

grafanaDashboards + prometheusAlerts
