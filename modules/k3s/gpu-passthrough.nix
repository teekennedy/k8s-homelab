{
  config,
  lib,
  ...
}:
lib.mkIf
(
  # Via `nixos-hardware.nixosModules.common-gpu-nvidia-nonprime`.
  builtins.elem "nvidia"
  config.services.xserver.videoDrivers
  # facter detected graphics
  or config.facter.detected.graphics.enable
)
{
  hardware = {
    nvidia-container-toolkit.enable = true;

    # Presence of Nvidia VAAPI driver adds `/run/opengl-driver` to CDI spec,
    # but that dir isn't created without this option -- which goes unset by
    # default on headless machines like k3s nodes.
    opengl.enable = true;
  };

  systemd.services = {
    nvidia-container-toolkit-cdi-generator = {
      # Even with `--library-search-path`, `nvidia-ctk` won't find the libs
      # unless I bodge their path into the environment.
      environment.LD_LIBRARY_PATH = "${config.hardware.nvidia.package}/lib";
    };
    k3s-containerd-setup = {
      # `virtualisation.containerd.settings` has no effect on k3s' bundled containerd.
      serviceConfig.Type = "oneshot";
      requiredBy = ["k3s.service"];
      before = ["k3s.service"];
      script = ''
        cat << EOF > /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl
        {{ template "base" . }}

        [plugins]
        "io.containerd.grpc.v1.cri".enable_cdi = true
        EOF
      '';
    };
  };

  nixpkgs.config.allowUnfreePredicate = pkg:
    builtins.elem (lib.getName pkg) [
      "nvidia"
      "nvidia-settings"
      "nvidia-x11"
    ];
}
