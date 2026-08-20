FROM rust:1.97-alpine AS build

WORKDIR /src
RUN apk add --no-cache musl-dev
COPY Cargo.toml Cargo.lock ./
COPY . .
ARG M365_BUILD_VERSION=dev
ARG M365_BUILD_COMMIT=dev
ARG M365_BUILD_TIME=unknown
ENV M365_BUILD_VERSION=${M365_BUILD_VERSION} \
    M365_BUILD_COMMIT=${M365_BUILD_COMMIT} \
    M365_BUILD_TIME=${M365_BUILD_TIME}
RUN cargo build --locked --release \
    && mkdir -p /out \
    && cp target/release/m365-native /out/m365-native

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && addgroup -S m365 && adduser -S -G m365 m365 \
    && mkdir -p /data /app
WORKDIR /app
COPY --from=build /out/m365-native /app/m365-native
COPY --from=build /src/web /app/web
RUN chown -R m365:m365 /app /data
USER m365
EXPOSE 4141
ENV M365_LISTEN=0.0.0.0:4141 \
    M365_DATA_DIR=/data \
    M365_CONFIG=/data/accounts.json \
    M365_TOKEN_CACHE=/data/token-cache.json \
    M365_SESSION_CACHE=/data/sessions.json \
    M365_API_KEYS=/data/api-keys.json \
    M365_ADMIN_PASSWORD_FILE=/data/admin-password \
    M365_ADMIN_PASSWORD_BOOTSTRAP_FILE=/run/secrets/m365_admin_password
VOLUME ["/data"]
ENTRYPOINT ["/app/m365-native"]
