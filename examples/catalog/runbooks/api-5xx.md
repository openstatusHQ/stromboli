# api: elevated 5xx

1. Check whether errors are concentrated on one endpoint or all of them.
2. Database errors (`too many clients`, timeouts) usually come from db-proxy: check its pool stats.
3. If a deploy went out in the last hour, compare its timing with the first error. Roll back with the previous image tag if they line up.
