# User Manager

- PostgreSQL table management service
- Does user organization + permissions + projects recordings + billing tables
- must implement storagemanager's `Authorizer` (may this user read/write files for this owner or project?): org existence and membership, project ownership, and project sharing with allowed users. Other services only see that interface, never these tables. In the monolith it's passed in-process; distributed, over gRPC
