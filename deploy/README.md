# Deploy

How the system is packaged and run. Cloud resources themselves live in `infra/`.

- `monolith/`: `gyoza` binary + document-server + PostgreSQL (+ MinIO for local S3)
- `distributed/`: one container/ECS task per service
