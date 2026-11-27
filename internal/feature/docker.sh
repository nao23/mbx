# Container DNS goes through dnsmasq in the VM: microsandbox's DNS returns SERVFAIL for non-EDNS answers
# over 512 bytes, which breaks musl (Alpine) containers. AAAA is dropped, as containers have no IPv6 route
mountpoint -q /var/lib/docker || exit 0
docker info >/dev/null 2>&1 && exit 0
upstream=$(awk '/^nameserver/ && $2 ~ /\./ {print $2; exit}' /etc/resolv.conf)
pidof dnsmasq >/dev/null || dnsmasq --no-resolv --server="$upstream" --listen-address=172.17.0.1 --bind-dynamic \
  --filter-AAAA --edns-packet-max=512 --no-hosts --cache-size=1000
# /run outlives a stop, and dockerd refuses to start while its old pid file names a live process (the pid
# is reused after the restart)
rm -f /run/docker.pid /run/docker/containerd/containerd.pid
# dockerd refuses dns set both by flag and in daemon.json
dns="--dns 172.17.0.1"
grep -qs '"dns"' /etc/docker/daemon.json && dns=
nohup dockerd $dns >/var/log/dockerd.log 2>&1 &
for i in $(seq 1 30); do docker info >/dev/null 2>&1 && exit 0; sleep 0.5; done
echo "warning: dockerd did not start (see /var/log/dockerd.log)" >&2
