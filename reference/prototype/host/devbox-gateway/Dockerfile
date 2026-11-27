FROM node:24-alpine
WORKDIR /app
COPY server.js boot.html ./
ENV NODE_ENV=production
EXPOSE 8080 2222
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/__devbox/status >/dev/null || exit 1
CMD ["node", "server.js"]
