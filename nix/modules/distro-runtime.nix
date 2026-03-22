{ lib, vivaryPackages ? null, ... }:

{
  users.groups.vivary = { };
  users.users.vivary = {
    isSystemUser = true;
    group        = "vivary";
    description  = "VIVARY runtime user";
    home         = "/var/lib/vivary";
    createHome   = false;
  };

  # Runtime directory layout.
  systemd.tmpfiles.rules = [
    "d /var/lib/vivary              0750 vivary vivary -"
    "d /var/lib/vivary/agents       0750 vivary vivary -"
    "d /var/lib/vivary/templates    0750 vivary vivary -"
    "d /var/log/vivary              0750 vivary vivary -"
    "d /etc/vivary                  0750 root   root   -"

    # LSB helper-binary directory: ward and cap-cli live here so keeperd can
    # bind-mount them read-only into each nspawn container at a known path.
    "d /usr/lib/vivary              0755 root   root   -"
  ] ++ lib.optionals (vivaryPackages != null) [
    # Symlink the two internal helpers to their canonical LSB paths.
    # keeperd hard-codes /usr/lib/vivary/ward as the bind-mount source.
    "L+ /usr/lib/vivary/ward    - - - - ${vivaryPackages}/bin/ward"
    "L+ /usr/lib/vivary/cap-cli - - - - ${vivaryPackages}/bin/cap-cli"
  ];

  # User-facing binaries (keeperd, vivary, vivary-log) land in PATH via
  # environment.systemPackages → /run/current-system/sw/bin/.
  environment.systemPackages = lib.optionals (vivaryPackages != null) [
    vivaryPackages
  ];

  environment.etc."vivary/runtime-profile".text = ''
    runtime-user=vivary
    runtime-home=/var/lib/vivary
    runtime-logs=/var/log/vivary
    ward-path=/usr/lib/vivary/ward
    cap-cli-path=/usr/lib/vivary/cap-cli
  '';
}
