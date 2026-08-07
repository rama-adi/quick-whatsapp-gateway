# Keystore volume backup and restore

The WhatsApp keystore is gateway-local SQLite cryptographic state. This procedure is a best-effort,
operator-managed recovery path; it does not provide automatic failover or a recovery-point objective.

1. Drain and stop the gateway. Confirm it has quiesced WA work and completed its WAL checkpoint/close.
2. Snapshot or copy the complete stopped persistent keystore volume. Never copy just a live WAL-mode
   SQLite main file and call it a backup.
3. Encrypt and protect the snapshot as credential material.
4. Restore the complete volume only while its previous gateway is stopped. Validate the restored SQLite
   file with the gateway's `OpenExisting` integrity check before device adoption.
5. Obtain a new fenced assignment epoch before starting any restored assigned session.

The control stream does not yet report these health details. Until the desired-state runtime consumes the
keystore seam, operators must use gateway startup errors and local validation for recovery decisions.
