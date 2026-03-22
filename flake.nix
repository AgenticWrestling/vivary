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

      # Build all VIVARY runtime binaries from source via buildGoModule.
      # On first build set vendorHash = lib.fakeHash, run `nix build`, then
      # replace with the sha256 printed in the error message.
      mkVivaryPackages = pkgs: pkgs.buildGoModule {
        pname = "vivary";
        version = "0.1.0-dev";
        src = lib.cleanSource ./.;
        vendorHash = lib.fakeHash;
        subPackages = [
          "cmd/keeperd"
          "cmd/vivary"
          "cmd/vivary-log"
          "cmd/ward"
          "cmd/cap-cli"
        ];
        # vivary-gen is a dev/codegen tool; it is not shipped in the runtime image.
      };

      mkImagePackage = { pkgs, rootfs, metadata, name, readme }:
        pkgs.runCommand name
          { nativeBuildInputs = [ pkgs.coreutils ]; }
          ''
            mkdir -p "$out"
            ln -s ${rootfs}   "$out/${name}-rootfs.tar.xz"
            ln -s ${metadata} "$out/${name}-metadata.tar.xz"
            cat > "$out/README.txt" <<'READMEEOF'
${readme}
READMEEOF
          '';
    in {
      formatter = forAllSystems (system: nixpkgs.legacyPackages.${system}.nixfmt-rfc-style);

      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in {
          default = pkgs.mkShell {
            packages = with pkgs; [ go go-task nixfmt-rfc-style ];
          };
        });

      # Internal NixOS system configurations used to derive LXD image tarballs.
      # Named with a system suffix to avoid clashing with the standard nixosConfigurations
      # convention (which expects named machines, not per-system outputs).
      nixosConfigurationsBase = forAllSystems (system:
        lib.nixosSystem {
          inherit system;
          modules = [ ./nix/modules/distro-base.nix ];
        });

      nixosConfigurationsRuntime = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in
        lib.nixosSystem {
          inherit system;
          specialArgs = { vivaryPackages = mkVivaryPackages pkgs; };
          modules = [
            ./nix/modules/distro-base.nix
            ./nix/modules/distro-runtime.nix
          ];
        });

      packages = forAllSystems (system:
        let
          pkgs      = nixpkgs.legacyPackages.${system};
          baseCfg   = self.nixosConfigurationsBase.${system};
          rtCfg     = self.nixosConfigurationsRuntime.${system};
        in {
          # vivary — Go binaries only (useful for CI artefacts independent of an image build)
          vivary = mkVivaryPackages pkgs;

          # distrobuild — minimal headless NixOS LXC base image, no VIVARY binaries
          distrobuild = mkImagePackage {
            inherit pkgs;
            rootfs   = baseCfg.config.system.build.images.lxc;
            metadata = baseCfg.config.system.build.images.lxc-metadata;
            name     = "vivary-lxc-base";
            readme   = ''
VIVARY distrobuild — base image

Import into LXD:

  lxc image import vivary-lxc-base-metadata.tar.xz \
                   vivary-lxc-base-rootfs.tar.xz \
                   --alias vivary-base

Then launch (see scripts/distro-lxd.sh launch):

  lxc launch vivary-base vivary \
    --config security.nesting=true \
    --config linux.kernel.modules=overlay,nf_tables

Minimal headless NixOS.  Includes: btrfs-progs, nftables, cacert.
Chromium runs at the host OS layer, not inside this image.
            '';
          };

          # distrobuild-runtime — base image + VIVARY binaries at LSB paths
          distrobuild-runtime = mkImagePackage {
            inherit pkgs;
            rootfs   = rtCfg.config.system.build.images.lxc;
            metadata = rtCfg.config.system.build.images.lxc-metadata;
            name     = "vivary-lxc-runtime";
            readme   = ''
VIVARY distrobuild — runtime image

Import into LXD:

  lxc image import vivary-lxc-runtime-metadata.tar.xz \
                   vivary-lxc-runtime-rootfs.tar.xz \
                   --alias vivary-runtime

Then launch (see scripts/distro-lxd.sh launch):

  scripts/distro-lxd.sh launch vivary-runtime vivary

Includes the base image plus: keeperd, vivary, vivary-log at /usr/bin/;
ward and cap-cli at /usr/lib/vivary/ for nspawn bind-mounting.
Chromium runs at the host OS layer.
            '';
          };

          default = self.packages.${system}.distrobuild;
        });
    };
}
