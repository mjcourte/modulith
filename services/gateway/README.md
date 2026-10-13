# Gateway

Dependency: storage, IAM, authz, NATS, documents

Merged from the former `rest-middleware` and `nats-ws-middleware`. Previously Node.js; planned in Go.

## REST

- gateway for REST calls from frontend into VPC

## WebSocket

- Basically how we redistribute message broker to FE clients

## Auth

- auth callout to SuperTokens was handled at the middleware layer
- REST and WS share one session layer, which is the main reason for merging the two middlewares
