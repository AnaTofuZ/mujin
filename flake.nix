{
  description = "Coder workspace fleet CLI for Claude Code";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  inputs.go-overlay.url = "github:purpleclay/go-overlay";
  inputs.go-overlay.inputs.nixpkgs.follows = "nixpkgs";

  outputs = { self, nixpkgs, go-overlay }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" "x86_64-darwin" ];
      eachSystem = f: nixpkgs.lib.genAttrs systems (system:
        let pkgs = import nixpkgs { inherit system; overlays = [ go-overlay.overlays.default ]; };
        in f pkgs (pkgs.go-bin.fromGoMod ./go.mod));
    in {
      packages = eachSystem (pkgs: go: {
        default = (pkgs.buildGoModule.override { inherit go; }) {
          pname = "mujin";
          version = "0.1.0";
          src = pkgs.lib.cleanSource ./.;
          vendorHash = "sha256-kn3uuOHipeoKk2Dbczj5iUSYUwYUZYrg4XlgLbAFKC4=";
          subPackages = [ "cmd/mujin" ];
          nativeCheckInputs = [ pkgs.git ];
          ldflags = [ "-s" "-w" ];
          meta = { description = "Coder workspace fleet CLI for Claude Code"; mainProgram = "mujin"; platforms = systems; };
        };
      });
      devShells = eachSystem (pkgs: go: {
        default = pkgs.mkShell {
          # coder CLI はサーバーと版を合わせるため、サーバーの install.sh で入れる
          packages = [ go pkgs.gopls pkgs.jq ];
          # mise などが外で設定した GOROOT が残ると、別の版の標準ライブラリを使ってしまう
          shellHook = "unset GOROOT";
        };
      });
    };
}
