#!/usr/bin/env bash
# install.sh — symlink agr into ~/.local/bin (Mac side).
set -euo pipefail

src="$(cd "$(dirname "$0")" && pwd)/agr"
dest_dir="${XDG_BIN_HOME:-$HOME/.local/bin}"
dest="$dest_dir/agr"

mkdir -p "$dest_dir"
ln -sf "$src" "$dest"
chmod +x "$src"

echo "linked $dest -> $src"
case ":$PATH:" in
  *":$dest_dir:"*) ;;
  *) echo "note: add $dest_dir to your PATH" ;;
esac
