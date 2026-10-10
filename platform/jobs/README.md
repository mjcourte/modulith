# Jobs

Formerly `services/job-queue`.

- NATS + pgSQL async job queue
- Planned as a Go library that services import rather than a standalone service, since JetStream + PostgreSQL already provide the queue. Revisit if it needs its own deployable.
