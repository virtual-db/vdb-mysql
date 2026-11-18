# Managed cloud CA bundles

One PEM file per provider (`aws-rds.pem`, `gcp-cloudsql.pem`, `azure-mysql.pem`), copied into the image at
`/usr/share/vdb/ca/` and trusted for Source TLS in addition to the system store (implementation plan doc 10 §8).
The weekly `ca-bundles.yml` workflow downloads each provider's published bundle and opens a pull request when one
changed.
