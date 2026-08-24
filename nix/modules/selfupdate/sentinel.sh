# Body of nixos-reboot-sentinel.service. Runs as root, oneshot.
#
# Creates the kured sentinel file if there's a new derivation staged for boot.

booted=$(readlink -f /run/booted-system)
staged=$(readlink -f /nix/var/nix/profiles/system)

if [ "$booted" = "$staged" ]; then
  echo "booted generation is already the staged one; not creating $SENTINEL_FILE"
  exit 0
fi

touch "$SENTINEL_FILE"
echo "created $SENTINEL_FILE; kured will drain and reboot this node"
