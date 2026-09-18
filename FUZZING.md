# Fuzzing

The badges below describe upstream infrastructure. This fork's required active
fuzzing runs in its own ClusterFuzzLite PR workflow; see [PATCHES.md](PATCHES.md).

[![Documentation](https://img.shields.io/badge/OSS--Fuzz-Introspector-red?style=flat)](https://introspector.oss-fuzz.com/project-profile?project=quic-go)
[![ClusterFuzz coverage](https://img.shields.io/codecov/c/github/quic-go/quic-go/master.svg?flag=clusterfuzz&label=ClusterFuzz%20coverage&logo=codecov&logoColor=white&style=flat)](https://app.codecov.io/gh/quic-go/quic-go?flags%5B0%5D=clusterfuzz)
[![ClusterFuzz Lite Batch coverage](https://img.shields.io/codecov/c/github/quic-go/quic-go/master.svg?flag=clusterfuzz-lite-batch&label=ClusterFuzz%20Lite%20Batch%20coverage&logo=codecov&logoColor=white&style=flat)](https://app.codecov.io/gh/quic-go/quic-go?flags%5B0%5D=clusterfuzz-lite-batch)

Run the commands below from a local [`google/oss-fuzz`](https://github.com/google/oss-fuzz) checkout.
Fuzz target names match the binary names listed in `oss-fuzz.sh` (for example, `frame_fuzzer_v2`).
Set `QUIC_SOURCE_DIR` to the absolute path of this fork's checkout. Every build
below mounts it at the path owned by Google's native `quic-go` project fixture.
That container path is independent of the Go module identity. The fixture's
`build.sh` invokes `oss-fuzz.sh` there; this script locates source relative to
itself and prints the tested commit. Without the mount, the image tests its
bundled official source instead of this fork.

```sh
export QUIC_SOURCE_DIR=/absolute/path/to/this/quic-go-checkout
```

Update the base images:
```sh
python3 infra/helper.py pull_images
```

## Running fuzzers locally

The following steps run a single fuzz target and then open its line-by-line coverage in `go tool cover`.

```sh
export DOCKER_DEFAULT_PLATFORM=linux/amd64
export FUZZ_TARGET=<fuzz_target>
export CORPUS_DIR=corpus/$FUZZ_TARGET

mkdir -p "$CORPUS_DIR"

python3 infra/helper.py build_image --no-pull quic-go
python3 infra/helper.py build_fuzzers --sanitizer address --mount_path /root/go/src/github.com/quic-go/quic-go quic-go "$QUIC_SOURCE_DIR"
python3 infra/helper.py run_fuzzer --corpus-dir="$CORPUS_DIR" quic-go "$FUZZ_TARGET"
```

Leave `run_fuzzer` running for a while to build up a corpus. It unpacks the seed corpus zip into the corpus directory and appends new entries as it discovers them.

```sh
python3 infra/helper.py build_fuzzers --sanitizer coverage --mount_path /root/go/src/github.com/quic-go/quic-go quic-go "$QUIC_SOURCE_DIR"
python3 infra/helper.py coverage --no-serve --fuzz-target "$FUZZ_TARGET" --corpus-dir="$CORPUS_DIR" quic-go
sed "s#^/out/#$(pwd)/build/out/quic-go/#" build/out/quic-go/fuzz.cov > "/tmp/quic-go-$FUZZ_TARGET.coverprofile"
go tool cover -html="/tmp/quic-go-$FUZZ_TARGET.coverprofile"
```

The `sed` command rewrites the container paths in `fuzz.cov` so that `go tool cover` can locate the source files in the local checkout.

The coverage build uses the same mounted checkout as the address-sanitizer build.

## Reproducing an OSS-Fuzz testcase

Download the reproducer file from the OSS-Fuzz report. To test a local fix, rebuild the fuzzers with this checkout mounted at the path expected by the native project fixture:

```sh
export DOCKER_DEFAULT_PLATFORM=linux/amd64
export FUZZ_TARGET=<fuzz_target>

python3 infra/helper.py build_image --no-pull quic-go
python3 infra/helper.py build_fuzzers --sanitizer address --mount_path /root/go/src/github.com/quic-go/quic-go quic-go "$QUIC_SOURCE_DIR"
python3 infra/helper.py reproduce quic-go "$FUZZ_TARGET" <reproducer_file>
```
