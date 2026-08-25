`Clone` copies the map but still shares each slice with the caller. Make the
result fully independent while preserving nil map and nil slice semantics. Add
regression coverage and run the package tests.
