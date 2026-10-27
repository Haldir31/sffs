#!/bin/bash
# Watchdog (cron toutes les 5 min) : relance start.sh si Silo, le montage sffs ou le pont
# ne répondent plus, deux fois à 30 s d'écart. Désactivation : touch $SFFS_STACK/STOPPED
. "$(dirname "$0")/common.sh"
[ -e "$STACK/STOPPED" ] && exit 0
mkdir -p "$RUN"; exec 9>"$RUN/.watchdog.lock"; flock -n 9 || exit 0
LOG=$RUN/watchdog.log
MNT=$(sffs_mount)
healthy() {
  health && mountpoint -q "$MNT" &&
  { [ "${SF_DB_BRIDGE:-0}" != 1 ] || pg_isready -q -h "$RUN/sfpg" -t 5; }
}
healthy && exit 0
sleep 30; healthy && exit 0
echo "$(date) - pile en panne, start.sh" >> "$LOG"
"$(dirname "$0")/start.sh" >> "$LOG" 2>&1 && echo "$(date) - rétablie" >> "$LOG" || echo "$(date) - toujours en panne" >> "$LOG"
