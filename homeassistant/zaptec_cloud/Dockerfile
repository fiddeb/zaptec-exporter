FROM golang:1.27.3-bookworm AS exporter
WORKDIR /src
# The packaging script includes the current checkout, not another branch.
ADD exporter-source.tar ./
RUN go test ./...
RUN CGO_ENABLED=0 go build -trimpath -o /zaptec-exporter .

FROM grafana/alloy:v1.21.3 AS alloy
FROM python:3.13-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libsystemd0 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=exporter /zaptec-exporter /usr/local/bin/zaptec-exporter
COPY --from=alloy /bin/alloy /usr/local/bin/alloy
COPY run.py /usr/local/bin/run.py
COPY config.alloy /etc/zaptec/config.alloy
ENV PYTHONUNBUFFERED=1
EXPOSE 8080
ENTRYPOINT ["python3", "/usr/local/bin/run.py"]
