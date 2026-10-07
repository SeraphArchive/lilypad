# Golden fixtures

These files are **synthetic, hand-anonymized** request/response examples — **not** real captured
traffic. They exist so unit/CI tests have deterministic, committable oracles without shipping any real
account data.

- Each file is `{ "route", "request", "response" }` with obviously-fake values.
- The full behavioral oracle (real capture) is loaded at test time from `LILYPAD_FIXTURES` and is never
  committed; tests that need it skip gracefully when it is absent.

When adding a golden, keep every value synthetic (no real XUIDs, tokens, codes, or balances).
