{
  description = "lab - unified CLI for k8s-homelab management";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      packages = {
        default = self.packages.${system}.lab;
        lab = pkgs.buildGoModule {
          pname = "lab";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-FZs7ObsTrjk8EK8lGSoubX9I2tnOH7V4CnvKZAfNnvw=";

          # Install shell completions
          postInstall = ''
            installShellCompletion --cmd lab \
              --bash <($out/bin/lab completion bash) \
              --zsh <($out/bin/lab completion zsh) \
              --fish <($out/bin/lab completion fish)
          '';

          nativeBuildInputs = with pkgs; [installShellFiles];

          meta = with pkgs.lib; {
            description = "Unified CLI for k8s-homelab management";
            homepage = "https://github.com/teekennedy/homelab";
            license = licenses.mit;
            maintainers = [];
            mainProgram = "lab";
          };
        };
      };

      apps.default = {
        type = "app";
        program = "${self.packages.${system}.lab}/bin/lab";
      };
    });
}
