# Registered with the F flag so containers can use the emulator too. qemu-user-binfmt has no entry for the VM's own
# architecture, so native binaries are unaffected
[ -e /proc/sys/fs/binfmt_misc/register ] || mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc
for conf in /usr/lib/binfmt.d/qemu-*.conf; do
  [ -e "/proc/sys/fs/binfmt_misc/$(basename "$conf" .conf)" ] || cat "$conf" >/proc/sys/fs/binfmt_misc/register
done
