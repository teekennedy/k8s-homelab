# UPS

The cluster runs on a CyberPower CP1500PFCRM2U with an RMCARD205 network management card. When the UPS has been on battery for 60 seconds, every node powers itself off.

## How it works

Each node runs its own copy of [NUT](https://networkupstools.org/) (`nix/modules/ups`), so no node depends on another to learn about a power failure:

1. The `snmp-ups` driver polls the card over SNMPv3 every 5 seconds.
2. On `ONBATT`, `upssched` starts a 60 second timer. `ONLINE` cancels it, so brief utility blips are ignored.
3. When the timer fires, `upsmon -c fsd` runs `shutdown now`. Kubelet's graceful node shutdown inhibitor gives pods up to 120 seconds to terminate before the host powers off.
4. A low battery or lost contact with the card while on battery shuts the node down immediately.

The nodes never tell the UPS to turn off its outlets (NUT's "killpower"); the first node to finish would cut power to the others. Turning the outlets off is left to the card, see [power-failure settings](#power-failure).

## Card settings

Set these in the card's web UI as the admin user. The card allows only one web session at a time; if it reports that someone is already logged in, wait for that session to time out (about 10 minutes).

### Network services

- **NTP**: enable it with Cloudflare's anycast servers `162.159.200.1` and `162.159.200.123`. The UniFi gateway doesn't answer NTP. Without NTP, the clock drifts badly across outages.
- **SNMPv1**: give the `public` community read-only access restricted to Home Assistant's IP, and set `private` to no access. Remove v1 entirely once Home Assistant reads the UPS over SNMPv3.
- **SNMPv3**: see [below](#snmpv3-credentials). The card restricts a user to a single IP, not a subnet, so the user is unrestricted; the firewall limits SNMP to the k8s nodes and Home Assistant.

### Power-failure

- **Turn the UPS off after 10 minutes on battery**, and **turn it back on when utility power returns**. Nodes begin shutting down at 1 minute and are off by about 4 minutes (60 s timer + up to 120 s pod drain + OS shutdown), so 10 minutes leaves margin. Runtime at the usual ~25% load is about 26 minutes.
- **Low battery threshold**: leave at least 5 minutes of runtime, since NUT shuts down immediately on low battery.

Cutting the outlets is what lets the nodes come back on their own: set the BIOS on each node to power on when AC power is restored. If utility power returns between the nodes shutting down and the 10 minute mark, the UPS never cuts its outlets and the nodes have to be powered on by hand.

### Battery tests

- Schedule a **monthly self-test**. It runs on battery for about 10 seconds, which the 60 second timer ignores.
- **Don't schedule runtime calibration.** It runs on battery for many minutes and shuts down the cluster.

## SNMPv3 credentials

1. In the card's SNMPv3 settings, create a user with **SHA** authentication and **AES** privacy, and separate auth and privacy passwords.
2. From a k8s node (the only hosts the firewall lets through), check that it works:

   ```sh
   nix shell nixpkgs#net-snmp -c snmpget -v3 -l authPriv \
     -u <user> -a SHA -A '<auth password>' -x AES -X '<privacy password>' \
     10.69.110.15 1.3.6.1.4.1.3808.1.1.1.4.1.1.0
   ```

   `INTEGER: 2` means the UPS is on line power.
3. Store the credentials with `sops edit nix/modules/ups/secrets.enc.yaml`:

   ```yaml
   ups_snmp_username: <user>
   ups_snmp_auth_password: <auth password>
   ups_snmp_priv_password: <privacy password>
   # Local upsd login for upsmon; any random string, e.g. `openssl rand -hex 24`
   upsmon_password: <random>
   ```

   Passwords may contain any characters; the module escapes them for NUT.

## Testing

Run these after deploying to all nodes.

1. **Every node can read the UPS.** On each node, `upsc cyberpower@localhost ups.status` prints `OL`, and `systemctl status upsdrv upsd upsmon` shows all three running. Driver errors are in `journalctl -u upsdrv`.
2. **The shutdown path works on one node.** On borg-3 (an agent, so etcd keeps quorum), run `sudo upsmon -c fsd`. This skips the timer and shuts down the same way a power failure does. After powering it back on, `journalctl -b -1 -u upsmon` shows the forced shutdown, and `journalctl -b -1 -u k3s | grep -i shutdown` shows kubelet draining pods.
3. **A blip doesn't shut anything down.** Start a battery self-test from the card. Each node's `journalctl -u upsmon` logs "on battery" then "on line power", and `journalctl -t upssched-cmd` stays empty.
4. **A real outage shuts everything down.** At a time when downtime is acceptable, unplug the UPS from the wall and leave it unplugged. Follow `journalctl -fu upsmon` on any node: every node logs "on battery", then about 60 seconds later `upssched-cmd` logs "UPS on battery for 60s, shutting down" and the node powers off. To also test the automatic power-on, leave the UPS unplugged past the 10 minute mark so it cuts its outlets, then plug it back in; the nodes should boot once the outlets come back. Otherwise, plug it back in once all nodes are off and power them on by hand.
