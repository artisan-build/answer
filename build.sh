#!/usr/bin/env bash
set -euo pipefail

version=2.0.3
plugins=a575b18c298a4e6b57875d2805d3b614fad980d3
mkdir -p .build
arch="$(uname -m)"
case "$arch" in
  aarch64|arm64) arch=arm64 ;;
  x86_64) arch=amd64 ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac

file="apache-answer-${version}-bin-linux-${arch}.tar.gz"
curl -fsSL "https://github.com/apache/answer/releases/download/v${version}/${file}" -o ".build/${file}"
case "$arch" in
  arm64) checksum=294b86f31d8b43651b8ac5c8369942f92f3174acc9b490f546c5166af8a700ef ;;
  amd64) checksum=a0196013cb2a8805ca75ebb89b45dfbe784483661f6e0b495f15f04d0277fcfa ;;
esac
echo "$checksum  .build/$file" | sha256sum -c -
tar -xzf ".build/$file" -C .build

# The official plugin builder needs Node and pnpm as well as Go.
if ! command -v node >/dev/null; then
  curl -fsSL "https://nodejs.org/dist/v24.15.0/node-v24.15.0-linux-${arch}.tar.xz" -o .build/node.tar.xz
  tar -xJf .build/node.tar.xz -C .build
  export PATH="$PWD/.build/node-v24.15.0-linux-${arch}/bin:$PATH"
fi
if ! command -v pnpm >/dev/null; then
  npm install --prefix "$PWD/.build/tools" pnpm@9.7.0
  export PATH="$PWD/.build/tools/node_modules/.bin:$PATH"
fi

git clone --no-checkout https://github.com/apache/answer-plugins.git .build/plugins
git -C .build/plugins checkout "$plugins"
git -C .build/plugins apply "$PWD/plugins.patch"

CGO_ENABLED=0 ".build/apache-answer-${version}-bin-linux-${arch}/answer" build \
  --with "github.com/apache/answer-plugins/storage-s3=$PWD/.build/plugins/storage-s3" \
  --with "github.com/apache/answer-plugins/cache-redis=$PWD/.build/plugins/cache-redis" \
  --output "$PWD/answer"
test -x answer
./answer plugin
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o app .
rm -rf .build
