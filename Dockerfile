# One image, three roles. fly.toml runs `migrate` before each release, then `api` and
# `worker` as separate process groups. See docs/deploy.md.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# A static binary with the time zone database built in: the weekly digest is scheduled
# in Australia/Sydney, and the runtime image holds nothing but the binary and CA roots.
RUN CGO_ENABLED=0 go build -trimpath -tags timetzdata -ldflags="-s -w" -o /out/vellatry ./cmd/vellatry

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/vellatry /vellatry
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/vellatry"]
CMD ["api"]
