{...}: {
  services.nfs-mtls = {
    enable = true;
    serverMode = true;
    clientMode = true;
  };
}
