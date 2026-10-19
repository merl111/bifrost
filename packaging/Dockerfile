# Build both frontends in the same Linux baseline; no host CUI checkout is used.
FROM golang:1.26.0-bookworm AS go
FROM ubuntu:24.04 AS toolchain
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git build-essential cmake pkg-config libgtk-4-dev \
    libxext-dev librsvg2-dev libgirepository1.0-dev file patchelf \
    desktop-file-utils python3 xvfb xauth x11-utils libxtst6 fonts-dejavu-core \
    dbus-x11 squashfs-tools && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH GOMAXPROCS=4
WORKDIR /src
RUN git init cui && cd cui && git remote add origin https://github.com/merl111/cui.git \
    && git fetch --depth=1 origin 1c05d5c43077af7524a171a127e60b3740c48775 \
    && git checkout --detach FETCH_HEAD
COPY patches/ /src/bifrost/patches/
RUN cd cui && git apply ../bifrost/patches/cui-desktop.patch \
    && git apply ../bifrost/patches/cui-picker-lifetime.patch \
    && cmake -S . -B build -DCMAKE_BUILD_TYPE=Release -DCUI_BUILD_SHARED=OFF \
       -DCUI_BUILD_EXAMPLES=OFF -DCUI_BUILD_TESTS=OFF -DCUI_BUILD_BINDING_TESTS=OFF \
    && cmake --build build --target cui --parallel 4
WORKDIR /src/bifrost
COPY go.mod go.sum ./
COPY desktop/go.mod desktop/go.sum ./desktop/
RUN go mod download && cd desktop && go mod download

FROM toolchain AS build
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build bash packaging/check.sh
RUN --mount=type=cache,target=/root/.cache/go-build bash packaging/build.sh /out

# No system GTK, Go, compiler or source checkout: smoke-test the shipped files.
FROM ubuntu:24.04 AS smoke-runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates python3 xvfb xauth x11-utils libxtst6 fonts-dejavu-core \
    dbus-x11 libgl1 libegl1 libgl1-mesa-dri && rm -rf /var/lib/apt/lists/*
FROM smoke-runtime AS verified
COPY --from=build /out /out
COPY packaging/smoke.py /smoke.py
RUN ! ldconfig -p | grep -q libgtk-4
RUN python3 /smoke.py tui /out/bifrost-*-linux-x86_64 \
    && dbus-run-session -- xvfb-run -a -s '-screen 0 1920x1080x24' \
       python3 /smoke.py gui /out/Bifrost-*-x86_64.AppImage
WORKDIR /out
RUN sha256sum --check SHA256SUMS
CMD ["/bin/true"]
