# Document Server

- Rust (native to whatever the rendering engine is)
- basically Kyoso-server
- transacts/log rolls snapshots in S3
- server side rendering
- does load balancing
- Document puts to S3 might want to be storage-ledger transacted, so the files are in pgSQL ledger
- This was previously build on Axum. But we could look at Dioxus or Leptos for consistency

## Crates, from memory

- main service binary crate
- load balancer/router binary crate
- types/contracts crate
- reusable data structures/classes (eg limited debounce executor)
- Rust-NATS integration, as a crate (in case we swap broker stack)
- AWS SDKs managed by crates (in case we swap Postgres, Dynamo, S3 for something else)
