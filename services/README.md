# Services

Lets make some organizational rules:

- Each package in services maps to one deployable microservice, when distributed.
  - Eg presently there are two for storage. they must be merged an internal organized.
- Shared code belongs in root/platform (infra and external tooling) or root/internal
- Make use of package/internal/domains folder structure when package/\*.go is >20 or 30 files.
  - Eg IAM has like 45 files. needs subfoldering

## Why "services" in Go, not "internal"?

- These particular internal packages are the modules of the modular monolith.
