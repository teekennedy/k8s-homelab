# Sets up a NFS server configured with mTLS authentication
{...}: {
  options = {};
  config = {
    # Load the kernel TLS module
    boot.kernelModules = ["tls"];
  };
}
