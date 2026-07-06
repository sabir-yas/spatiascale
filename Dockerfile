FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /spatialscale-server ./cmd/spatialscale-server

FROM gcr.io/distroless/static-debian12
COPY --from=build /spatialscale-server /spatialscale-server
COPY testdata/transcripts_sample.csv /data/transcripts.csv
ENTRYPOINT ["/spatialscale-server", "-csv", "/data/transcripts.csv", "-addr", ":50051"]
