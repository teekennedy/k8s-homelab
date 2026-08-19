{
  lib,
  buildGoModule,
  installShellFiles,
}:
buildGoModule {
  pname = "lab";
  version = "0.2.4";
  src = ./.;

  # vendorHash lives in gomod.json so it can be rewritten programmatically
  # when Renovate bumps a Go dependency. Regenerate with:
  #   dagger generate homelab:update-go-vendor-hash -y
  vendorHash = (lib.importJSON ./gomod.json).vendorHash;

  postInstall = ''
    installShellCompletion --cmd lab \
      --bash <($out/bin/lab completion bash) \
      --zsh <($out/bin/lab completion zsh) \
      --fish <($out/bin/lab completion fish)
  '';

  nativeBuildInputs = [installShellFiles];

  meta = with lib; {
    description = "Unified CLI for k8s-homelab management";
    homepage = "https://github.com/teekennedy/homelab";
    license = licenses.mit;
    maintainers = [];
    mainProgram = "lab";
  };
}
