{...}: {
  services.nfs-mtls = {
    enable = true;
    clientMode = true;
  };
}
