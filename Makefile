export GOAMD64 = v3
export GOTELEMETRY = off

.PHONY: anime-to-seerr-blocklist clean

anime-to-seerr-blocklist:
	go build -trimpath -gcflags="all=-C -dwarf=false" -ldflags="-s -w -buildid=" -buildvcs=false

clean:
	-go clean -i
