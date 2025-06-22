resource "terraform_data" "create_namespace" {
  input = var.namespace

  provisioner "local-exec" {
    command = <<EOF
      count="$(kubectl get namespaces | grep -E '^${var.namespace}[^-[:alnum:]]' | wc -l)"
      if [ "$count" -eq "0" ]; then
        kubectl create namespace ${var.namespace}
      else
        echo 'namespace "${var.namespace}" already exists' >&2
      fi
    EOF
  }
}

resource "kubernetes_secret" "external" {
  metadata {
    name      = var.name
    namespace = terraform_data.create_namespace.output

    annotations = {
      "app.kubernetes.io/managed-by" = "Terraform"
    }
  }

  data = var.data
}
