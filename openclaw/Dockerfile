FROM docker.io/openclaw/openclaw:2026.9.7@sha256:0da12cd49983fcb5e4915fd3135ce7a33d82f93649b1df6964946d2c1d1dbcfc AS whisper-build

USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends cmake g++ make pkg-config libavcodec-dev libavformat-dev libavutil-dev libswresample-dev \
    && rm -rf /var/lib/apt/lists/* \
    && git clone https://github.com/ggml-org/whisper.cpp.git /build/whisper \
    && git -C /build/whisper checkout 927cfce34f31707e17f2bff35c349632fb9e2c3a
# whisper.cpp v1.9.4; FFmpeg handles Telegram OGG/Opus directly.
RUN cmake -S /build/whisper -B /build/whisper/build \
      -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF \
      -DWHISPER_COMMON_FFMPEG=ON -DWHISPER_BUILD_TESTS=OFF \
    && cmake --build /build/whisper/build --target whisper-cli -j2

FROM docker.io/openclaw/openclaw:2026.9.7@sha256:0da12cd49983fcb5e4915fd3135ce7a33d82f93649b1df6964946d2c1d1dbcfc
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg libavcodec59 libavformat59 libavutil57 libswresample4 libgomp1 \
    && command -v ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /opt/whisper \
    && curl --fail --location --silent --show-error \
      https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-small-q5_1.bin \
      --output /opt/whisper/ggml-small-q5_1.bin \
    && echo 'ae85e4a935d7a567bd102fe55afc16bb595bdb618e11b2fc7591bc08120411bb  /opt/whisper/ggml-small-q5_1.bin' | sha256sum -c -
COPY --from=whisper-build /build/whisper/build/bin/whisper-cli /usr/local/bin/whisper-cli
# The publisher targets linux/amd64. Verify the standalone CLI before extraction.
RUN curl --fail --location --silent --show-error \
      https://github.com/NVIDIA/OpenShell/releases/download/v0.1.2/openshell-x86_64-unknown-linux-musl.tar.gz \
      --output /tmp/openshell.tar.gz \
    && echo '7eb6917285331a09e3300266a0558616481a5e9927cae2612ea07c4045b6dd6f  /tmp/openshell.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/openshell.tar.gz -C /usr/local/bin openshell \
    && rm /tmp/openshell.tar.gz \
    && openshell --version
COPY openshell/package.json openshell/package-lock.json /opt/openclaw-plugins/openshell/
RUN npm ci --prefix /opt/openclaw-plugins/openshell --omit=dev --legacy-peer-deps --ignore-scripts \
    && npm cache clean --force
# Version-matched, dependency-free external plugin; keep it outside the runtime home.
RUN mkdir -p /opt/openclaw-plugins/parallel \
    && curl --fail --location --silent --show-error \
      https://registry.npmjs.org/@openclaw/parallel-plugin/-/parallel-plugin-2026.9.7.tgz \
      --output /tmp/parallel-plugin.tgz \
    && echo 'c60fa75fb357f6430ee637d938a21112ff1ec1841e34c3fbe137cc46233beb080f26dec5fb86fee5efd7fa374df086be3e764a9580c554a9f9b54cc4ea42098e  /tmp/parallel-plugin.tgz' | sha512sum -c - \
    && tar -xzf /tmp/parallel-plugin.tgz --strip-components=1 -C /opt/openclaw-plugins/parallel \
    && rm /tmp/parallel-plugin.tgz \
    && chown -R node:node /opt/openclaw-plugins
USER node
