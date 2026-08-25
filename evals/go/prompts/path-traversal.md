`ReadDocument` must only read regular files below its configured root. A caller
can currently escape the root with a relative path. Close traversal and absolute
path variants without using a fragile string-prefix check, then run the tests.
