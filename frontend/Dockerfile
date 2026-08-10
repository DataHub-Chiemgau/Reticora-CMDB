FROM node:20-alpine AS builder

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build

FROM nginx:alpine

COPY --from=builder /app/dist /usr/share/nginx/html
# nginx.conf is a complete main configuration (it sets the pid path for the
# unprivileged nginx user), so it must replace /etc/nginx/nginx.conf instead
# of being dropped into conf.d/ (where the `pid` directive is not allowed).
COPY nginx.conf /etc/nginx/nginx.conf
# Shared proxy settings for the Keycloak locations, included by the server
# blocks in nginx.conf and in the compose-mounted TLS template.
COPY keycloak-proxy.conf /etc/nginx/reticora-keycloak-proxy.conf

# When TLS is enabled, the nginx image entrypoint renders the compose-mounted
# /etc/nginx/templates/reticora-tls.conf.template into /etc/nginx/conf.d/
# (included by nginx.conf). The container runs as the unprivileged nginx user
# (docker-compose sets `user: nginx`), so that directory — and the log
# directory written via the compile-time default paths — must be writable.
# The stock default.conf server on port 80 is replaced by our own server
# block in nginx.conf.
RUN rm -f /etc/nginx/conf.d/default.conf \
    && chown -R nginx:nginx /etc/nginx/conf.d /var/log/nginx

EXPOSE 80
