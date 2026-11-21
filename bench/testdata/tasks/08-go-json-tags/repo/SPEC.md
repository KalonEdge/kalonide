# User JSON

A user is encoded as an object with exactly these keys:

| key | type | notes |
|---|---|---|
| `user_id` | number | |
| `full_name` | string | |
| `email` | string | omitted when empty |
| `created_at` | string | RFC 3339 time |

The internal `PasswordHash` field must never appear in the JSON.
