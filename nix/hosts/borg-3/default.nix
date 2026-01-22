{...}: {
  imports = [
    ../common/laptop.nix
    ../../modules/ci
  ];

  ci.runner = {
    enable = true;
    url = "https://git.msng.to";
    tokenFile = "/var/lib/gitea-runner/token";
    labels = [
      "ubuntu-latest:docker://node:20-bullseye"
    ];
    capacity = 1;
    extraGroups = [
      "ci-runner"
    ];
    containerOptions = [
      "--cpus=4"
      "--memory=6g"
      "--env=DOCKER_HOST=unix:///run/podman/podman.sock"
      "--volume=/run/podman/podman.sock:/run/podman/podman.sock"
    ];
    enableDocker = false;
    enablePodman = true;
  };
}
