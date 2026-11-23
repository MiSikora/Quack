#!/usr/bin/env bash
set -euo pipefail

# Builds quack and quack-mcp for release into cli/build/dist: a universal macOS archive and Linux amd64 and arm64
# archives for each binary. Each archive holds the binary, the license and the notices of the Go modules linked in.
# It needs macOS for lipo.

cd "$(dirname "$0")/.."

version=$(grep '^VERSION_NAME=' gradle.properties | cut -d= -f2)
dist="cli/build/dist"
work="cli/build/release"
rm -rf "$dist" "$work"
mkdir -p "$dist" "$work"

goLicenses="github.com/google/go-licenses/v2@v2.0.1"
noticesTemplate="$work/notices.tpl"
cat >"$noticesTemplate" <<'EOF'
# Third-party notices

{{ range . }}## {{ .Name }} {{ .Version }}

{{ .LicenseName }}

```
{{ .LicenseText }}
```

{{ end }}
EOF

function build {
  local binary=$1 os=$2 arch=$3 out=$4
  (cd cli && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-X quack/internal/command.Version=$version" -o "../$out" "./cmd/$binary")
}

function package {
  local binary=$1 platform=$2 executable=$3
  local dir="$work/$binary-$version-$platform"
  mkdir -p "$dir"
  cp "$executable" "$dir/$binary"
  cp LICENSE.md "$dir/"
  cp "$work/$binary-notices.md" "$dir/THIRD_PARTY_NOTICES.md"
  tar -czf "$dist/$binary-$version-$platform.tar.gz" -C "$work" "$binary-$version-$platform"
}

for binary in quack quack-mcp; do
  (cd cli && go run "$goLicenses" report "./cmd/$binary" --ignore quack --template "../$noticesTemplate") >"$work/$binary-notices.md"

  build "$binary" darwin arm64 "$work/$binary-darwin-arm64"
  build "$binary" darwin amd64 "$work/$binary-darwin-amd64"
  lipo -create -output "$work/$binary-macos" "$work/$binary-darwin-arm64" "$work/$binary-darwin-amd64"
  package "$binary" macos "$work/$binary-macos"

  for arch in amd64 arm64; do
    build "$binary" linux "$arch" "$work/$binary-linux-$arch"
    package "$binary" "linux-$arch" "$work/$binary-linux-$arch"
  done
done

ls -1 "$dist"
