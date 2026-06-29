# Notification delivery service

## Queue consumer

Add a component that consumes the notification queue and fans out to per-user
channels. The notification service retries with backoff on a failed send; a
banner field in the payload marks high-priority items.

## Indexing

Add a database index on the recipient column to speed lookups.
