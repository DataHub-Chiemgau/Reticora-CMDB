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

EXPOSE 80
