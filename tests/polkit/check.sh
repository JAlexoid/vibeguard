set -eu
mkdir -p /run/dbus
dbus-daemon --system --fork
/usr/lib/polkit-1/polkitd > /tmp/polkit.log 2>&1 &
trap 'cat /tmp/polkit.log' EXIT
for attempt in 1 2 3 4 5; do
 if pkaction --action-id org.freedesktop.systemd1.manage-unit-files >/dev/null 2>&1; then break; fi
 sleep 1
done
pkaction --version
sh /check-polkit vibeguard
echo 'PASS: actual polkit daemon authorizes unit-file management for vibeguard'
if runuser -u nobody -- sh -c 'pkcheck --action-id org.freedesktop.systemd1.manage-unit-files --process $$'; then exit 1; fi
echo 'PASS: unrelated account denied'
# A root-only file is invisible to the unprivileged polkitd reader. Restart only
# this container's daemon so the unreadable file is freshly evaluated.
chmod 600 /etc/polkit-1/rules.d/50-vibeguard.rules
pkill -x polkitd
/usr/lib/polkit-1/polkitd > /tmp/polkit.log 2>&1 &
sleep 1
if sh /check-polkit vibeguard; then
 echo 'Unreadable rule unexpectedly authorized the service account' >&2
 exit 1
fi
echo 'PASS: installer check detects an unreadable root-only rule'
