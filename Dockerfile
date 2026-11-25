FROM mirror.gcr.io/library/golang:1.27.4@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/underwraps ./cmd/underwraps
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/underwraps /underwraps
USER 65532:65532
ENTRYPOINT ["/underwraps"]
