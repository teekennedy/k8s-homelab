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
          vendorHash = "sha256-SOFLqMLssNBx1jgoQ40U1uOthNLS+SSwCxRzqttTYkU=";

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
