FROM node AS web-build
WORKDIR /broadcast-box/web
COPY . /broadcast-box
RUN npm install && npm run build

FROM golang:alpine AS go-build
WORKDIR /broadcast-box
COPY . /broadcast-box
RUN apk add git
RUN go build

FROM golang:alpine
COPY --from=web-build /broadcast-box/web/build /broadcast-box/web/build
COPY --from=go-build /broadcast-box/broadcast-box /broadcast-box/broadcast-box
COPY --from=go-build /broadcast-box/.env.production /broadcast-box/.env.production

ENV APP_ENV=production
ENV NETWORK_TEST_ON_START=true

WORKDIR /broadcast-box
ENTRYPOINT ["/broadcast-box/broadcast-box"]
