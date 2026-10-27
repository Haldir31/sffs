#!/bin/bash
# Désinstalle silosffs puis sffs par le mécanisme SSDV2 (conteneurs, DNS, registre).
# Les données (bases, affiches, secrets) restent dans <SETTINGS_STORAGE>/docker/$USER/ ;
# le rôle sffs_reader reste dans la base StreamFusion (DROP ROLE sffs_reader pour le retirer).
set -euo pipefail
. "$(dirname "$0")/lib.sh"
for app in silosffs sffs; do
  bash "$SETTINGS_SOURCE/includes/config/scripts/generique.sh" suppression_appli "$app" 0 || true   # 0 = garder les données
done
for m in /mnt/sffs/sf /mnt/sffs; do mountpoint -q "$m" && sudo umount -l "$m"; done; true
echo "Désinstallé (SSDV2 a aussi retiré vars/sffs.yml et vars/silosffs.yml ; ssd/install.sh les remet)."
