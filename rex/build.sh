#!/bin/bash
# Construit rex avec les patchs sffs (rex/patches) sur rex/BASE_COMMIT.
#
#   rex/build.sh [dossier de travail]      (défaut ~/.cache/sffs-build/rex)
#
# Binaire produit : <dossier>/target/release/rex. Le lancer avec REX_FEED_REPROBE=sffs (nom de la
# source sffs dans rex) pour que la vraie analyse envoyée par sffs remplace celle d'une version
# déjà connue. Compilation bridée (2 tâches, 4 processeurs) : sur un compte au nombre de
# processus plafonné, cargo et rustc lanceraient sinon un fil par processeur.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${1:-$HOME/.cache/sffs-build/rex}
BASE=$(cat "$HERE/BASE_COMMIT")
mkdir -p "$WORK"
[ -d "$WORK/src/.git" ] || git clone -q https://github.com/debridmediamanager/rex "$WORK/src"
cd "$WORK/src"
git fetch -q origin
git checkout -q -f -B sffs "$BASE"
git -c user.name=sffs -c user.email=sffs@localhost am -q "$HERE"/patches/*.patch
export CARGO_BUILD_JOBS=${CARGO_BUILD_JOBS:-2} RAYON_NUM_THREADS=${RAYON_NUM_THREADS:-2} CARGO_TARGET_DIR="$WORK/target"
run=(cargo build --release --manifest-path rust/Cargo.toml)
command -v taskset >/dev/null && run=(taskset -c 0-3 "${run[@]}")
"${run[@]}"
echo "rex construit : $WORK/target/release/rex (base $BASE + $(ls "$HERE"/patches/*.patch | wc -l) patch(s))"
