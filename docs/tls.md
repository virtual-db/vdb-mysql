# TLS

**To the Source** VDB always uses TLS and verifies the certificate chain and host name, as `VERIFY_IDENTITY` does,
against the system trust store plus the CA bundles of AWS RDS, Google Cloud SQL and Azure Database for MySQL that the
image carries. There is nothing to set up for a managed Source. A Source without TLS is refused.

**A self-signed Source** is refused at start, and the log names its certificate's SHA-256 fingerprint. If the Source
is yours, set `VDB_SOURCE_TLS_FINGERPRINT` to that value to trust exactly that certificate.

**From clients** `mysqld` offers TLS with its own generated certificate, or with yours from
`VDB_CLIENT_TLS_CERT_FILE` and `VDB_CLIENT_TLS_KEY_FILE`. Accounts that `REQUIRE SSL` on the Source require it in VDB.
