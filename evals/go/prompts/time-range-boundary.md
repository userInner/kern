`Window.Contains` currently includes the end instant, causing adjacent windows to
double-count events. Implement a half-open `[start,end)` interval, reject invalid
windows through the existing constructor, and run the boundary tests.
