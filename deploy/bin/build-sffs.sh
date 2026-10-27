#!/bin/bash
# Compile sffs vers $SFFS_STACK/bin/sffs.new (installé par start.sh/redeploy.sh au prochain démarrage,
# ou directement en bin/sffs à la première installation).
. "$(dirname "$0")/common.sh"
mkdir -p "$STACK/bin"
cd "$REPO/sffs"
go build -trimpath -o "$STACK/bin/sffs.new" . || exit 1
if [ ! -e "$STACK/bin/sffs" ]; then mv "$STACK/bin/sffs.new" "$STACK/bin/sffs"; echo "sffs installé : $STACK/bin/sffs"
else echo "nouvelle version prête : $STACK/bin/sffs.new (appliquée par redeploy.sh)"; fi
