{
  description = "Forcefield development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      forAllSystems = nixpkgs.lib.genAttrs systems;

    in {
      devShells = forAllSystems (system:
        let
          pkgs = import nixpkgs {
            inherit system;
          };
        in {
          default = pkgs.mkShell {
            name = "forcefield-dev";

            packages = with pkgs; [
              go
              gofumpt
              golangci-lint
              goreleaser
              git
              gnumake
            ];

            shellHook = ''
              echo "Forcefield development environment"
              echo "Go: $(go version)"
              echo ""
              echo "Available:"
              echo "  make build"
              echo "  make test"
              echo "  make lint"
              echo "  goreleaser"
            '';
          };
        });
    };
}