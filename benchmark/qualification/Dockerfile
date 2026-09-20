ARG GO_IMAGE
FROM ${GO_IMAGE}

ARG LOCAL_UID=1001
ARG LOCAL_GID=1001
RUN test "${LOCAL_UID}" -gt 0 && test "${LOCAL_GID}" -gt 0 \
    && groupadd --non-unique --gid "${LOCAL_GID}" qualifier \
    && useradd --non-unique --uid "${LOCAL_UID}" --gid qualifier --create-home qualifier \
    && mkdir -p /work /home/qualifier/go /home/qualifier/.cache/go-build \
    && chown -R qualifier:qualifier /work /home/qualifier
COPY worker.sh /usr/local/bin/qualify-worker.sh
RUN chmod 0555 /usr/local/bin/qualify-worker.sh
USER qualifier
WORKDIR /work
ENTRYPOINT ["/usr/bin/env", "-i", "PATH=/usr/local/go/bin:/home/qualifier/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/home/qualifier", "USER=qualifier", "LOGNAME=qualifier", "GOPATH=/home/qualifier/go", "GOCACHE=/home/qualifier/.cache/go-build", "GOTOOLCHAIN=local", "GOENV=off", "GOMAXPROCS=2", "CGO_ENABLED=1", "CI=true", "TZ=UTC", "LANG=C.UTF-8", "bash", "/usr/local/bin/qualify-worker.sh"]
