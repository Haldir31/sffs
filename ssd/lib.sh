# Fonctions communes aux scripts ssd/ (à sourcer) : repère l'installation SSDV2.
if [ -f "$HOME/.config/ssd/env" ]; then
  set -a; . "$HOME/.config/ssd/env"; set +a
fi
if [ -z "${SETTINGS_SOURCE:-}" ] && [ -f /opt/seedbox-compose/ssddb ]; then
  SETTINGS_SOURCE=/opt/seedbox-compose SETTINGS_STORAGE=/opt/seedbox
fi
[ -n "${SETTINGS_SOURCE:-}" ] && [ -d "$SETTINGS_SOURCE/includes/dockerapps" ] || {
  echo "Installation SSDV2 introuvable (~/.config/ssd/env ou /opt/seedbox-compose)." >&2; exit 1; }
export SETTINGS_SOURCE SETTINGS_STORAGE
export USER=${USER:-$(id -un)}
# Ansible du venv SSDV2 s'il existe (comme ssdv2ctl).
for d in "$SETTINGS_SOURCE/venv/bin" "$HOME/.venv/ssd/bin" "$SETTINGS_SOURCE/.venv/bin"; do
  [ -x "$d/ansible-playbook" ] && export PATH="$d:$PATH" && break
done

# Lance l'installation (ou la réinstallation) d'une application par le mécanisme SSDV2 standard.
ssd_launch() {
  bash "$SETTINGS_SOURCE/includes/config/scripts/generique.sh" launch_service "$1"
}

# Écrit une valeur dans account.yml (chiffré) par la fonction SSDV2.
ssd_account() {
  bash "$SETTINGS_SOURCE/includes/config/scripts/generique.sh" manage_account_yml "$1" "$2" >/dev/null
}

# Une application est « installée » si SSDV2 l'a enregistrée (ssddb, statut 2).
ssd_registered() {
  [ -f "$SETTINGS_SOURCE/ssddb" ] && command -v sqlite3 >/dev/null &&
    [ "$(sqlite3 "$SETTINGS_SOURCE/ssddb" "select status from applications where name='$1'" 2>/dev/null)" = 2 ]
}
running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }
