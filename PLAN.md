# The plan, but human

- start from storage. this covers all data in memory, postgres, and rustfs/s3. deal with both flat blob storage and the postgres ledger that can construct a file directory a la google drive/dropbox. This is divided into two packages, but its one microservice, if distributed. We also need the REST routes for put/get files from FE?
- next is iam. this will feed back slightly into storage w.r.t. permissions, but otherwise a boilerplate thing. Covers user/org/project CRUD. Need rest routes for cruds?
- "documents service" is the WAL compactor. it log rolls NATS + snapshots. I think i definitely want the dual delivery message type delta OR snapshot via NATS. basically a total postgres WAL impl. This will be the backend's peer and source of truth for document states in-memory
- exporter - fork the rendering functionality from the documents service
- somewhere next we will need to do NATS and gateway and supertokens.
- What is our trrrracing strategy?
- frontend uhhhh
- engine uhhhh yeah i mean just yolo redo CRDT + ECS + renderer? lmao ok
- agent uhhh leave this until last, at least just know we can basically graphQL or gRPC into documenter
