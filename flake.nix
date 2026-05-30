{
  description = "agency CLI for provisioning WordPress hosts, deploying themes, and syncing site data across nonfiction projects";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = inputs:
    inputs.flake-utils.lib.eachDefaultSystem (
      system: let
        inherit (inputs.flake-utils.lib) mkApp;
        pkgs = import inputs.nixpkgs {inherit system;};
        nf = pkgs.buildGoModule {
          pname = "nf";
          version = "0.1.0";
          src = ./.;
          modRoot = ".";
          subPackages = ["cmd/nf"];
          vendorHash = "sha256-DeRPEIZL//++6CBlY6WHSUQFywsNf4iHBvlHDmYCGpI=";
        };
      in {
        packages.default = nf;
        packages.nf = nf;

        apps.default = mkApp {
          drv = nf;
          exePath = "/bin/nf";
        };

        apps.nf = mkApp {
          drv = nf;
          exePath = "/bin/nf";
        };

        devShells.default = pkgs.mkShell {
          packages = [pkgs.go pkgs.gotools nf];
        };
      }
    );
}
