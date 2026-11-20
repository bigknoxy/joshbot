{
  # joshbot — a nanobot-class personal AI agent as a single static Go binary.
  #
  #   nix run github:bigknoxy/joshbot
  #
  # The flake serves the binary from the latest GitHub release, so what nix
  # runs is the same artifact the curl install fetches — no compile, no
  # dependency graph to hash. After a new release, bump `version` and the
  # four hashes below; `nix build` prints the hash it expected on a mismatch,
  # or recompute one directly:
  #
  #   nix hash file --sri joshbot_v<ver>_linux_amd64
  description = "A nanobot-class personal AI agent as a single ~19MB Go binary — no Python, no venv";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        version = "1.71.0";
        assets = {
          x86_64-linux = { asset = "joshbot_v${version}_linux_amd64"; hash = "sha256-UKPR+AE2zIq7QSMbuJ9cSsKplrtRM94B1wN1MM6YjV0="; };
          aarch64-linux = { asset = "joshbot_v${version}_linux_arm64"; hash = "sha256-HBRyR7a/5YNgzC4JVW5fvGI8MN4UYIHaMPsaqg4aAYo="; };
          x86_64-darwin = { asset = "joshbot_v${version}_darwin_amd64"; hash = "sha256-Acoz9nR3nz6AoCn7L5aDwG2U4cpGUt0DW6JsguE8zo4="; };
          aarch64-darwin = { asset = "joshbot_v${version}_darwin_arm64"; hash = "sha256-VJF9Tx8KRlPb3+D2yCbmUk7THfqKCD87CWNGRYPDNdY="; };
        };
      in
      rec {
        packages.default = packages.joshbot;
        packages.joshbot = pkgs.stdenv.mkDerivation {
          pname = "joshbot";
          inherit version;
          src = pkgs.fetchurl {
            url = "https://github.com/bigknoxy/joshbot/releases/download/v${version}/${assets.${system}.asset}";
            hash = assets.${system}.hash;
          };
          dontUnpack = true;
          dontConfigure = true;
          dontBuild = true;
          installPhase = ''
            runHook preInstall
            install -Dm755 $src $out/bin/joshbot
            runHook postInstall
          '';
          meta = with pkgs.lib; {
            description = "Nanobot-class personal AI agent as a single static Go binary";
            longDescription = ''
              A nanobot-class personal AI agent in one ~19MB static Go binary —
              no Python, no venv, no runtime dependencies. Self-hosted, curl to
              running, scriptable enough to test in CI.
            '';
            homepage = "https://github.com/bigknoxy/joshbot";
            license = licenses.mit;
            mainProgram = "joshbot";
            platforms = builtins.attrNames assets;
          };
        };
        apps.default = flake-utils.lib.mkApp { drv = packages.joshbot; };
      });
}
