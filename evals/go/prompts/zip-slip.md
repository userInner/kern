Archive extraction permits entries to escape the destination directory. Reject
absolute and traversal paths before writing, create safe parent directories, and
keep valid nested extraction working. Add regression coverage and run all tests.
