`DecodeRequest` reads an unbounded request body into memory. Enforce the declared
byte limit, reject oversized bodies instead of silently truncating them, retain
valid JSON behavior, and run the tests without adding dependencies.
