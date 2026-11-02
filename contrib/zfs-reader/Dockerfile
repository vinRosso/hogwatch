# The hogwatch-zfs sidecar: reads ZFS numbers through /dev/zfs and writes
# /out/zfs.tsv. Released builds use BASE=debian:13 with BACKPORTS=trixie, the
# newest OpenZFS userland; other bases (ubuntu:24.04, debian:12) work for a
# local build.
ARG BASE=ubuntu:24.04
# BACKPORTS is a Debian suite name (for example trixie): zfsutils-linux is then
# installed from that suite's backports, for hosts that run the newer ZFS from there.
ARG BACKPORTS=
FROM ${BASE}
ARG BACKPORTS
# On Debian zfsutils-linux lives in the "contrib" component.
RUN for f in /etc/apt/sources.list.d/debian.sources; do \
      [ -f "$f" ] && sed -i 's/^Components: main$/Components: main contrib/' "$f"; \
    done; \
    target=""; \
    if [ -n "$BACKPORTS" ]; then \
      echo "deb http://deb.debian.org/debian ${BACKPORTS}-backports main contrib" > /etc/apt/sources.list.d/backports.list; \
      target="-t ${BACKPORTS}-backports"; \
    fi; \
    apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends $target zfsutils-linux \
 && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /out && chown 10001:10001 /out
COPY write-zfs-tsv.sh entrypoint.sh /usr/local/bin/
RUN chmod 0755 /usr/local/bin/write-zfs-tsv.sh /usr/local/bin/entrypoint.sh
ENV ZFS_OUT=/out/zfs.tsv ZFS_INTERVAL=300
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
HEALTHCHECK --interval=60s --timeout=5s --start-period=60s CMD ["/usr/local/bin/entrypoint.sh", "healthcheck"]
