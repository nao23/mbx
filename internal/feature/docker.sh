# Container DNS goes through dnsmasq in the VM: microsandbox's DNS returns SERVFAIL for non-EDNS answers
# over 512 bytes, which breaks musl (Alpine) containers. AAAA is dropped, as containers have no IPv6 route
mountpoint -q /var/lib/docker || exit 0
docker info >/dev/null 2>&1 && exit 0
upstream=$(awk '/^nameserver/ && $2 ~ /\./ {print $2; exit}' /etc/resolv.conf)
pidof dnsmasq >/dev/null || dnsmasq --no-resolv --server="$upstream" --listen-address=172.17.0.1 --bind-dynamic \
  --filter-AAAA --edns-packet-max=512 --no-hosts --cache-size=1000

# Containers and docker build's RUN steps trust microsandbox's CA like the VM does, through mbx-runc.
# mbx-runc wraps runc: it mounts the CA files on the container's /dev, and points the usual CA variables
# at them unless the container sets them. /dev is a tmpfs, so nothing is left in a built image.
# Written while dockerd is down, so no mbx-runc is running
cat >/usr/local/bin/mbx-runc <<'EOF'
#!/bin/sh

# runc's create and run (the commands that start a container) take --bundle:
# the directory holding the container's config.json
bundle= prev=
for arg in "$@"; do
	[ "$prev" = --bundle ] && bundle=$arg
	prev=$arg
done

config=$bundle/config.json
ca=/usr/local/share/ca-certificates/microsandbox-ca.crt
# No CA without TLS interception
if [ -n "$bundle" ] && [ -f "$config" ] && [ -f "$ca" ]; then
	filter='
		def mount($src; $dst): {type: "bind", source: $src, destination: $dst, options: ["rbind", "ro"]};
		def setenv($name; $value): if any(.[]; startswith($name + "=")) then . else . + ["\($name)=\($value)"] end;

		.mounts += [
			mount("/etc/ssl/certs/ca-certificates.crt"; "/dev/mbx-ca-bundle.crt"),
			mount($ca; "/dev/mbx-ca.crt")
		]
		| .process.env |= (. // []
			| setenv("SSL_CERT_FILE"; "/dev/mbx-ca-bundle.crt")
			| setenv("GIT_SSL_CAINFO"; "/dev/mbx-ca-bundle.crt")
			| setenv("CURL_CA_BUNDLE"; "/dev/mbx-ca-bundle.crt")
			| setenv("REQUESTS_CA_BUNDLE"; "/dev/mbx-ca-bundle.crt")
			# Node adds these to its own CAs, so it gets the CA alone, as in the VM
			| setenv("NODE_EXTRA_CA_CERTS"; "/dev/mbx-ca.crt"))'

	if jq --arg ca "$ca" "$filter" "$config" >"$config.mbx"; then
		mv "$config.mbx" "$config"
	else
		rm -f "$config.mbx"
	fi
fi

exec runc "$@"
EOF
chmod +x /usr/local/bin/mbx-runc

# dockerd uses mbx-runc for containers through daemon.json's default-runtime, and for builds through
# DOCKER_BUILDKIT_RUNC_COMMAND, as its BuildKit runs runc itself
mkdir -p /etc/docker
[ -s /etc/docker/daemon.json ] || echo '{}' >/etc/docker/daemon.json
jq '.runtimes["mbx-runc"] = {path: "/usr/local/bin/mbx-runc"} | .["default-runtime"] //= "mbx-runc"' \
  /etc/docker/daemon.json >/etc/docker/daemon.json.tmp && mv /etc/docker/daemon.json.tmp /etc/docker/daemon.json

# /run outlives a stop, and dockerd refuses to start while its old pid file names a live process (the pid
# is reused after the restart)
rm -f /run/docker.pid /run/docker/containerd/containerd.pid

# dockerd refuses dns set both by flag and in daemon.json
dns="--dns 172.17.0.1"
grep -qs '"dns"' /etc/docker/daemon.json && dns=
DOCKER_BUILDKIT_RUNC_COMMAND=/usr/local/bin/mbx-runc nohup dockerd $dns >/var/log/dockerd.log 2>&1 &
for i in $(seq 1 30); do docker info >/dev/null 2>&1 && exit 0; sleep 0.5; done
echo "warning: dockerd did not start (see /var/log/dockerd.log)" >&2
