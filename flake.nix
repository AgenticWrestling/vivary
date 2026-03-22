{
  description = "VIVARY distrobuild flake";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
  };

  outputs = { self, nixpkgs }:
    let
      lib = nixpkgs.lib;
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = lib.genAttrs systems;
    in {
      formatter = forAllSystems (system: nixpkgs.legacyPackages.${system}.nixfmt-rfc-style);

      devShells = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              go-task
              nixfmt-rfc-style
            ];
          };
        });

      nixosConfigurations = forAllSystems (system:
        lib.nixosSystem {
          inherit system;
          modules = [
            ./nix/modules/distro-base.nix
          ];
        });

      packages = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          cfg = self.nixosConfigurations.${system};
          rootfs = cfg.config.system.build.images.lxc;
          metadata = cfg.config.system.build.images.lxc-metadata;
        in {
          distrobuild = pkgs.runCommand "vivary-distrobuild"
            {
              nativeBuildInputs = [ pkgs.coreutils ];
            }
            ''
              mkdir -p "$out"
              ln -s ${rootfs} "$out/vivary-lxc-rootfs.tar.xz"
              ln -s ${metadata} "$out/vivary-lxc-metadata.tar.xz"
              cat > "$out/README.txt" <<'EOF'
VIVARY distrobuild output

This directory contains the two tarballs needed to import the base image into LXD:

  - vivary-lxc-rootfs.tar.xz
  - vivary-lxc-metadata.tar.xz

Example:

  lxc image import vivary-lxc-metadata.tar.xz vivary-lxc-rootfs.tar.xz --alias vivary-base

The image is intentionally minimal and headless. It provides the base NixOS runtime,
systemd-nspawn support, Btrfs tooling, nftables, SQLite, and headless Chromium.
Project binaries are expected to be layered separately once the Go build is stable.
EOF
            '';

          default = self.packages.${system}.distrobuild;
        });
    };
}
