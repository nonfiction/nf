{
  description = "nf safe local CLI skeleton";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        nfPackage = pkgs.python3Packages.buildPythonApplication {
          pname = "nf";
          version = "0.1.0";
          src = ./.;
          format = "pyproject";
          nativeBuildInputs = with pkgs.python3Packages; [ setuptools wheel ];
        };
      in {
        packages.default = nfPackage;
        packages.nf = nfPackage;

        apps.default = flake-utils.lib.mkApp {
          drv = nfPackage;
        };

        apps.nf = flake-utils.lib.mkApp {
          drv = nfPackage;
        };

        devShells.default = pkgs.mkShell {
          packages = [ nfPackage ];
        };
      }
    );
}
