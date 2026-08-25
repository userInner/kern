The decoder cannot distinguish a missing `workers` field from an explicit zero,
so it overwrites the supported auto mode. Preserve the public `Config` shape,
default only an absent field, reject trailing JSON, and run all tests.
