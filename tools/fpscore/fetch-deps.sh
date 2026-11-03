#!/usr/bin/env bash
# Fetch SourceAFIS for Java (Apache-2.0) and its runtime dependencies from
# Maven Central into lib/. No Maven needed; versions are those SourceAFIS
# 3.18.1's POM pins. Re-running is a no-op once the jars are present.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p lib
M=https://repo1.maven.org/maven2
while read -r path; do
  f="lib/$(basename "$path")"
  [ -s "$f" ] || curl -fsSL "$M/$path" -o "$f"
done <<'JARS'
com/machinezoo/sourceafis/sourceafis/3.18.1/sourceafis-3.18.1.jar
com/machinezoo/stagean/stagean/1.3.0/stagean-1.3.0.jar
com/machinezoo/closeablescope/closeablescope/1.0.1/closeablescope-1.0.1.jar
com/machinezoo/noexception/noexception/1.9.1/noexception-1.9.1.jar
com/machinezoo/fingerprintio/fingerprintio/1.3.1/fingerprintio-1.3.1.jar
it/unimi/dsi/fastutil/8.5.12/fastutil-8.5.12.jar
commons-io/commons-io/2.15.0/commons-io-2.15.0.jar
com/google/code/gson/gson/2.10.1/gson-2.10.1.jar
com/fasterxml/jackson/core/jackson-databind/2.15.3/jackson-databind-2.15.3.jar
com/fasterxml/jackson/core/jackson-core/2.15.3/jackson-core-2.15.3.jar
com/fasterxml/jackson/core/jackson-annotations/2.15.3/jackson-annotations-2.15.3.jar
com/fasterxml/jackson/dataformat/jackson-dataformat-cbor/2.15.3/jackson-dataformat-cbor-2.15.3.jar
com/github/mhshams/jnbis/2.1.2/jnbis-2.1.2.jar
org/slf4j/slf4j-api/2.0.9/slf4j-api-2.0.9.jar
JARS
ls lib | wc -l
