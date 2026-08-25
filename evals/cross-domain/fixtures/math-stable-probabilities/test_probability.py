import math
import unittest

from probability import logsumexp, softmax


class ProbabilityTests(unittest.TestCase):
    def test_large_and_small_values_are_stable(self):
        self.assertAlmostEqual(logsumexp([1000.0, 1000.0]), 1000.0 + math.log(2), places=12)
        self.assertAlmostEqual(logsumexp([-1000.0, -1001.0]), -999.6867383124818, places=12)
        values = softmax([1000.0, 1001.0, 999.0])
        self.assertTrue(all(math.isfinite(value) and value >= 0 for value in values))
        self.assertAlmostEqual(sum(values), 1.0, places=15)

    def test_empty_and_infinite_inputs(self):
        self.assertEqual(logsumexp([]), -math.inf)
        self.assertEqual(softmax([]), [])
        self.assertEqual(logsumexp([-math.inf, -math.inf]), -math.inf)
        self.assertEqual(softmax([1.0, math.inf, 2.0]), [0.0, 1.0, 0.0])
        self.assertEqual(softmax([math.inf, 1.0, math.inf]), [0.5, 0.0, 0.5])

    def test_nan_is_rejected(self):
        with self.assertRaises(ValueError): logsumexp([0.0, math.nan])
        with self.assertRaises(ValueError): softmax([math.nan])


if __name__ == "__main__": unittest.main()
