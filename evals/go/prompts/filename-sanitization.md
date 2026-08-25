Upload filenames are trusted after `filepath.Base`, which does not reject every
platform's separators. Reject absolute, traversal, slash and backslash forms;
accept simple portable basenames; preserve the signature and run all tests.
