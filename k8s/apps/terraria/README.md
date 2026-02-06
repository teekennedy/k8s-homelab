# Terraria (TShock)

This application deploys a TShock-based Terraria server using the bjw-s-labs `app-template` Helm chart.

## What gets deployed

- A single `terraria` pod running `ghcr.io/pryaxis/tshock:sha256-1b6af2e9741e2ea24268a553a3699a0010344f02d6fa13d099357d72dd2edba2`
- Persistent volumes mounted at:
  - `/tshock`
  - `/worlds`
  - `/plugins`
- Service ports:
  - `7777/TCP` for Terraria game traffic
  - `7878/TCP` as the TShock REST API (`http` service port)
- HTTP ingress for the REST API:
  - Host: `terraria.msng.to`
  - ExternalDNS CNAME target: `external.network.msng.to`
  - No TLS configuration (HTTP entrypoint)
- A Traefik TCP route named `terrariapublic` that forwards external traffic on entrypoint `terrariapublic` to the game server on port `7777`

## First-time setup

1. Deploy/sync the Argo CD application:
   - `lab k8s --env production sync terraria`
2. Verify the pod and PVCs:
   - `kubectl -n terraria get pods,pvc`
3. Confirm Traefik has the TCP route:
   - `kubectl -n terraria get ingressroutetcp terrariapublic`

## Configuration notes

- The server starts with:
  - `-world /worlds/backflip.wld`
  - `-motd "HEYOOOO"`
- Ensure `/worlds/backflip.wld` exists in the `worlds` PVC (or update `values.yaml` to point to a different world file).
- To customize the server (MOTD, world path, storage sizes, image tag), edit `values.yaml` and re-sync the app.
