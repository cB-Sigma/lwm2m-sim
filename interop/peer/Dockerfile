# Copyright 2026 Abhinav Gautam
# SPDX-License-Identifier: Apache-2.0
#
# Eclipse Leshan's demo client, demo server and demo bootstrap server
# (EPL-2.0 or EDL-1.0), downloaded from Maven Central at a pinned version
# and checked against its SHA-256. Used as a test peer only; nothing of
# Leshan is kept in this repository.
FROM eclipse-temurin:21-jre-alpine

ARG LESHAN=2.0.0-M18
ARG MAVEN=https://repo1.maven.org/maven2/org/eclipse/leshan

WORKDIR /leshan
RUN wget -q -O client.jar "$MAVEN/leshan-demo-client/$LESHAN/leshan-demo-client-$LESHAN-jar-with-dependencies.jar" \
 && wget -q -O server.jar "$MAVEN/leshan-demo-server/$LESHAN/leshan-demo-server-$LESHAN-jar-with-dependencies.jar" \
 && wget -q -O bsserver.jar "$MAVEN/leshan-demo-bsserver/$LESHAN/leshan-demo-bsserver-$LESHAN-jar-with-dependencies.jar" \
 && echo "7519d31d94ef053aa9ccd269a12169b4d46f17cd7ffcdf1af437137114a59443  client.jar" > sums \
 && echo "734b5b8487aa208847b676e7f543744998c6b6ab7bc154551220336d633b02da  server.jar" >> sums \
 && echo "972a955e063afe4c01c9579fb1a8f292eaf12b2794fd6b4870d78a149c6bcfb3  bsserver.jar" >> sums \
 && sha256sum -c sums
