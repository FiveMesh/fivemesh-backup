.PHONY: build test vet check release-artifacts

build:
	go build -trimpath -o bin/fivemesh-backup ./cmd/fivemesh-backup

test:
	go test ./...

vet:
	go vet ./...

check: vet test

release-artifacts:
	@test -n "$(VERSION)" || (echo "VERSION is required" >&2; exit 1)
	@test -n "$(COMMIT)" || (echo "COMMIT is required" >&2; exit 1)
	@test -n "$(BUILT_AT)" || (echo "BUILT_AT is required" >&2; exit 1)
	mkdir -p dist/v$(VERSION)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X github.com/FiveMesh/fivemesh-backup/internal/version.Version=$(VERSION) -X github.com/FiveMesh/fivemesh-backup/internal/version.Commit=$(COMMIT) -X github.com/FiveMesh/fivemesh-backup/internal/version.Date=$(BUILT_AT)" \
		-o dist/v$(VERSION)/fivemesh-backup-linux-amd64 ./cmd/fivemesh-backup
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
		-ldflags "-s -w -X github.com/FiveMesh/fivemesh-backup/internal/version.Version=$(VERSION) -X github.com/FiveMesh/fivemesh-backup/internal/version.Commit=$(COMMIT) -X github.com/FiveMesh/fivemesh-backup/internal/version.Date=$(BUILT_AT)" \
		-o dist/v$(VERSION)/fivemesh-backup-linux-arm64 ./cmd/fivemesh-backup
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X github.com/FiveMesh/fivemesh-backup/internal/version.Version=$(VERSION) -X github.com/FiveMesh/fivemesh-backup/internal/version.Commit=$(COMMIT) -X github.com/FiveMesh/fivemesh-backup/internal/version.Date=$(BUILT_AT)" \
		-o dist/v$(VERSION)/fivemesh-backup-windows-amd64.exe ./cmd/fivemesh-backup
	cp install/install.sh install/install.ps1 dist/v$(VERSION)/
	cd dist/v$(VERSION) && sha256sum fivemesh-backup-* install.sh install.ps1 > SHA256SUMS

