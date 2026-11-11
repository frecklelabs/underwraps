FROM mirror.gcr.io/library/golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/underwraps ./cmd/underwraps
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/underwraps /underwraps
USER 65532:65532
ENTRYPOINT ["/underwraps"]
