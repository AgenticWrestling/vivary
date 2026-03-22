{ lib, pkgs, modulesPath, ... }:

let
  maxAgents = 20;
  uidBlockSize = 65536;
  subidCount = maxAgents * uidBlockSize;
in {
  imports = [
    (modulesPath + "/virtualisation/lxc-container.nix")
  ];

  boot.isContainer = true;

  networking.hostName = "vivary";
  networking.useDHCP = lib.mkDefault true;

  time.timeZone = "UTC";
  i18n.defaultLocale = "C.UTF-8";
  console.keyMap = "us";

  documentation.enable = false;
  documentation.info.enable = false;
  documentation.man.enable = false;
  programs.command-not-found.enable = false;

  environment.defaultPackages = lib.mkForce [ ];
  environment.systemPackages = with pkgs; [
    bashInteractive
    btrfs-progs
    cacert
    nftables
    sqlite
  ];

  services.openssh.enable = false;
  services.udisks2.enable = false;

  security.sudo.enable = false;
  users.mutableUsers = false;
  users.users.root.initialHashedPassword = "!";

  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    auto-optimise-store = true;
  };

  environment.etc."subuid".text = "root:100000:${toString subidCount}\n";
  environment.etc."subgid".text = "root:100000:${toString subidCount}\n";

  systemd.extraConfig = ''
    DefaultTimeoutStopSec=15s
  '';

  system.stateVersion = "25.11";
}
