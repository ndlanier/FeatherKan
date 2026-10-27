FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN mkdir -p /data && chmod 700 /data && go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /featherkan .

FROM scratch
COPY --from=build /featherkan /featherkan
# Provide a writable directory for a newly created named volume.
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/featherkan"]
CMD ["-listen", "0.0.0.0:8080", "-data", "/data"]
