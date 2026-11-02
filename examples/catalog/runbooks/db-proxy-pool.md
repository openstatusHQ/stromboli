# db-proxy: connection pool exhausted

Symptoms: `connection pool exhausted` in db-proxy, `too many clients` in callers.

1. Look for a caller that recently raised its worker or connection count.
2. Mitigation: restart the noisy caller with its previous config, or raise `max` temporarily (Postgres allows 200).
3. Do not restart db-proxy itself first: it drops every in-flight query.
