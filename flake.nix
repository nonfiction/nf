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
        nfPackage = pkgs.buildGoModule {
          pname = "nf";
          version = "0.1.0";
          src = ./.;
          modRoot = ".";
          subPackages = [ "cmd/nf" ];
          vendorHash = "sha256-JGQ/UHaGj8t8G/stfcTTnGtifw8ZfbxCzByzH5METyo=";
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
          packages = [ pkgs.go pkgs.gotools nfPackage ];
        };
      }
    );
}
