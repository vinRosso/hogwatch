# The hogwatch-zfs sidecar: reads ZFS numbers through /dev/zfs and writes
# /out/zfs.tsv. BASE must match the host distro so the zfs userland matches
# the kernel module, for example ubuntu:24.04, debian:12 or debian:13.
ARG BASE=ubuntu:24.04
FROM ${BASE}
# On Debian zfsutils-linux lives in the "contrib" component.
RUN for f in /etc/apt/sources.list.d/debian.sources; do \
      [ -f "$f" ] && sed -i 's/^Components: main$/Components: main contrib/' "$f"; \
    done; \
    apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends zfsutils-linux \
 && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /out && chown 10001:10001 /out
COPY write-zfs-tsv.sh entrypoint.sh /usr/local/bin/
RUN chmod 0755 /usr/local/bin/write-zfs-tsv.sh /usr/local/bin/entrypoint.sh
ENV ZFS_OUT=/out/zfs.tsv ZFS_INTERVAL=300
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
HEALTHCHECK --interval=60s --timeout=5s --start-period=60s CMD ["/usr/local/bin/entrypoint.sh", "healthcheck"]
