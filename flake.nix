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
        version = pkgs.lib.strings.trim (builtins.readFile ./internal/version/VERSION);
        versionDate = builtins.substring 0 4 version + "-" + builtins.substring 5 2 version + "-" + builtins.substring 8 2 version;
        nf = pkgs.buildGoModule {
          pname = "nf";
          inherit version;
          src = ./.;
          modRoot = ".";
          subPackages = ["cmd/nf"];
          vendorHash = "sha256-DeRPEIZL//++6CBlY6WHSUQFywsNf4iHBvlHDmYCGpI=";
          ldflags = [
            "-s"
            "-w"
            "-X github.com/nonfiction/nf/internal/version.Version=${version}"
            "-X github.com/nonfiction/nf/internal/version.Commit=${inputs.self.shortRev or "unknown"}"
            "-X github.com/nonfiction/nf/internal/version.Date=${versionDate}"
          ];
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
          version="$(cd "$repo" && go run ./cmd/nf version --short)"
          commit="$(cd "$repo" && git rev-parse --short HEAD 2>/dev/null || printf unknown)"
          date="''${version:0:4}-''${version:5:2}-''${version:8:2}"
          ldflags="-X github.com/nonfiction/nf/internal/version.Version=$version -X github.com/nonfiction/nf/internal/version.Commit=$commit -X github.com/nonfiction/nf/internal/version.Date=$date"
          (cd "$repo" && go build -trimpath -ldflags "$ldflags" -o $HOME/.local/bin/nf ./cmd/nf)
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
