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
        nf-build = pkgs.writeShellScriptBin "nf-build" ''
          set -eu

          repo="$PWD"
          while [ "$repo" != "/" ] && { [ ! -f "$repo/go.mod" ] || [ ! -d "$repo/cmd/nf" ]; }; do
            repo="$(dirname "$repo")"
          done

          if [ ! -f "$repo/go.mod" ] || [ ! -d "$repo/cmd/nf" ]; then
            echo "nf-build: run from the nf repo or one of its subdirectories" >&2
            exit 1
          fi

          mkdir -p "$HOME/.local/bin/"
          (cd "$repo" && go build -o $HOME/.local/bin/nf ./cmd/nf)
          echo "built $HOME/.local/bin/nf"
        '';
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
          packages = [pkgs.go pkgs.gotools nf nf-build];
          shellHook = ''
            echo "nf dev: run nf-build to refresh ~/.local/bin/nf"
          '';
        };
      }
    );
}
