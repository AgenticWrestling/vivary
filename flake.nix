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

      # Build all VIVARY runtime binaries from source.
      # On first build set vendorHash = lib.fakeHash, run `nix build`, then
      # substitute the sha256 printed in the error message.
      mkVivaryPackages = pkgs: pkgs.buildGoModule {
        pname   = "vivary";
        version = "0.1.0-dev";
        src     = lib.cleanSource ./.;
        env.CGO_ENABLED = "0";
        vendorHash = "sha256-YomkA1EJXjAkWAvDYzIieSdw8CQzSeck+3zhjTGSqQI=";
        subPackages = [
          "cmd/keeperd"
          "cmd/viv"
          "cmd/vivlog"
          "cmd/ward"
          "cmd/capwrap"
        ];
        # vivary-gen is a dev/codegen tool; not shipped in the runtime image.
      };

      # Generate a minimal LXD-compatible metadata tarball.
      # LXD expects metadata.yaml to be YAML, not JSON with a YAML filename.
      # When the file is JSON, LXD does not read creation_date and rejects the
      # import with "Missing creation date".
      mkLxdMetadata = { pkgs, system, description }:
        let
          arch = { "x86_64-linux" = "x86_64"; "aarch64-linux" = "aarch64"; }.${system};
        in
        pkgs.runCommand "lxd-metadata.tar.xz" {
          nativeBuildInputs = [ pkgs.gnutar pkgs.xz ];
        } ''
          mkdir tmp
          cat > tmp/metadata.yaml <<EOF
architecture: ${arch}
creation_date: 1704067200
properties:
  description: ${description}
  os: nixos
  release: "25.11"
templates: {}
EOF
          tar -C tmp -cJf "$out" metadata.yaml
        '';

      # Bundle a NixOS rootfs tarball and a generated LXD metadata tarball
      # into a single derivation output the distro-lxd.sh script can use.
      mkImagePackage = { pkgs, system, nixosCfg, name, readme }:
        let
          # system.build.tarball is produced by lxc-container.nix via
          # make-system-tarball.nix; it puts the archive in $drv/tarball/.
          rootfsDrv = nixosCfg.config.system.build.tarball;
          metaDrv   = mkLxdMetadata { inherit pkgs system; description = name; };
        in
        pkgs.runCommand name {
          nativeBuildInputs = [ pkgs.coreutils ];
        } ''
          mkdir -p "$out"
          # There is exactly one .tar.xz in the tarball/ subdirectory.
          rootfs=$(echo ${rootfsDrv}/tarball/*.tar.xz)
          ln -s "$rootfs"  "$out/${name}-rootfs.tar.xz"
          ln -s ${metaDrv} "$out/${name}-metadata.tar.xz"
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
          pkgs    = nixpkgs.legacyPackages.${system};
          baseCfg = self.nixosConfigurationsBase.${system};
          rtCfg   = self.nixosConfigurationsRuntime.${system};
        in {
          # Go binaries only — useful as a CI artefact independent of the image build.
          vivary = mkVivaryPackages pkgs;

          # Minimal headless NixOS LXC base image; no VIVARY binaries.
          distrobuild = mkImagePackage {
            inherit pkgs system;
            nixosCfg = baseCfg;
            name     = "vivary-lxc-base";
            readme   = ''
VIVARY distrobuild — base image

Import into LXD:

  lxc image import vivary-lxc-base-metadata.tar.xz \
                   vivary-lxc-base-rootfs.tar.xz \
                   --alias vivary-base

Then launch:

  scripts/distro-lxd.sh launch vivary-base vivary

Minimal headless NixOS. Includes: btrfs-progs, nftables, cacert.
Chromium runs at the host OS layer, not inside this image.
            '';
          };

          # Base image + VIVARY binaries at LSB paths.
          distrobuild-runtime = mkImagePackage {
            inherit pkgs system;
            nixosCfg = rtCfg;
            name     = "vivary-lxc-runtime";
            readme   = ''
VIVARY distrobuild — runtime image

Import into LXD:

  lxc image import vivary-lxc-runtime-metadata.tar.xz \
                   vivary-lxc-runtime-rootfs.tar.xz \
                   --alias vivary-runtime

Then launch:

  scripts/distro-lxd.sh launch vivary-runtime vivary

Includes base image plus: keeperd, viv, vivlog in PATH;
ward and capwrap at /usr/lib/vivary/ for agent-rootfs bind-mounting into /usr/bin/.
Chromium runs at the host OS layer.
            '';
          };

          default = self.packages.${system}.distrobuild;
        });
    };
}
