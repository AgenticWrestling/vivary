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
      mkVivaryRuntimePackage = pkgs:
        let
          repoRoot = ./.;
          binaryNames = [ "keeperd" "ward" "vivary" "vivary-log" ];
          binariesPresent = lib.all (name: builtins.pathExists (repoRoot + "/bin/${name}")) binaryNames;
        in
          if binariesPresent then
            pkgs.stdenvNoCC.mkDerivation {
              pname = "vivary-runtime-binaries";
              version = "dev";
              src = repoRoot + "/bin";
              dontUnpack = true;
              installPhase = ''
                mkdir -p "$out/bin"
                install -m755 "$src/keeperd" "$out/bin/keeperd"
                install -m755 "$src/ward" "$out/bin/ward"
                install -m755 "$src/vivary" "$out/bin/vivary"
                install -m755 "$src/vivary-log" "$out/bin/vivary-log"
              '';
            }
          else
            pkgs.runCommandNoCC "vivary-runtime-binaries-unavailable" { } ''
              echo "missing runtime binaries in ./bin; run go-task build once the Go build is stable" >&2
              exit 1
            '';
      mkImagePackage = { pkgs, rootfs, metadata, name, readme }:
        pkgs.runCommand name
          {
            nativeBuildInputs = [ pkgs.coreutils ];
          }
          ''
            mkdir -p "$out"
            ln -s ${rootfs} "$out/${name}-rootfs.tar.xz"
            ln -s ${metadata} "$out/${name}-metadata.tar.xz"
            cat > "$out/README.txt" <<'EOF'
${readme}
EOF
          '';
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

      nixosConfigurationsRuntime = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
          lib.nixosSystem {
            inherit system;
            specialArgs = {
              vivaryRuntimePackage = mkVivaryRuntimePackage pkgs;
            };
            modules = [
              ./nix/modules/distro-base.nix
              ./nix/modules/distro-runtime.nix
            ];
          });

      packages = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          baseCfg = self.nixosConfigurations.${system};
          runtimeCfg = self.nixosConfigurationsRuntime.${system};
        in {
          distrobuild = mkImagePackage {
            inherit pkgs;
            rootfs = baseCfg.config.system.build.images.lxc;
            metadata = baseCfg.config.system.build.images.lxc-metadata;
            name = "vivary-lxc-base";
            readme = ''
VIVARY distrobuild output

This directory contains the two tarballs needed to import the base image into LXD:

  - vivary-lxc-base-rootfs.tar.xz
  - vivary-lxc-base-metadata.tar.xz

Example:

  lxc image import vivary-lxc-base-metadata.tar.xz vivary-lxc-base-rootfs.tar.xz --alias vivary-base

The image is intentionally minimal and headless. It provides the base NixOS runtime,
systemd-nspawn support, Btrfs tooling, nftables, and SQLite.
Chromium is intentionally not included here because browser execution happens at the host OS layer,
outside the NixOS LXD guest.
Project binaries are expected to be layered separately once the Go build is stable.
            '';
          };

          distrobuild-runtime = mkImagePackage {
            inherit pkgs;
            rootfs = runtimeCfg.config.system.build.images.lxc;
            metadata = runtimeCfg.config.system.build.images.lxc-metadata;
            name = "vivary-lxc-runtime";
            readme = ''
VIVARY runtime distrobuild output

This directory contains the two tarballs needed to import the runtime image into LXD:

  - vivary-lxc-runtime-rootfs.tar.xz
  - vivary-lxc-runtime-metadata.tar.xz

Example:

  lxc image import vivary-lxc-runtime-metadata.tar.xz vivary-lxc-runtime-rootfs.tar.xz --alias vivary-runtime

This image extends the minimal headless base image with the VIVARY runtime filesystem layout
and the locally prepared VIVARY binaries from ./bin.
If the binaries are not present yet, this build will fail with an explicit message.
Chromium is still intentionally excluded because browser execution belongs to the host OS layer.
            '';
          };

          default = self.packages.${system}.distrobuild;
        });
    };
}
