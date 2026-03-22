{ lib, vivaryRuntimePackage ? null, ... }:

{
  users.groups.vivary = { };
  users.users.vivary = {
    isSystemUser = true;
    group = "vivary";
    description = "VIVARY runtime user";
    home = "/var/lib/vivary";
    createHome = false;
  };

  systemd.tmpfiles.rules = [
    "d /var/lib/vivary 0750 vivary vivary -"
    "d /var/lib/vivary/agents 0750 vivary vivary -"
    "d /var/lib/vivary/templates 0750 vivary vivary -"
    "d /var/lib/vivary/runtime 0750 vivary vivary -"
    "d /var/log/vivary 0750 vivary vivary -"
    "d /etc/vivary 0750 root root -"
  ];

  environment.systemPackages = lib.optionals (vivaryRuntimePackage != null) [
    vivaryRuntimePackage
  ];

  environment.etc."vivary/runtime-profile".text = ''
    runtime-user=vivary
    runtime-home=/var/lib/vivary
    runtime-logs=/var/log/vivary
  '';
}
