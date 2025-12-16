#!/usr/bin/env bash

set -euxo pipefail

nix --version
hugo version
sass --version
go version
firebase --version
make --version
dprint --version
typos --version
magick --version
actionlint --version
ls --version
nixfmt --version
nixd --version
peco --version
vim --version | sed -n '1p'
rumdl --version
