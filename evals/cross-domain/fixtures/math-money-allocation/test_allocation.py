from decimal import Decimal
import unittest

from allocation import allocate


class AllocationTests(unittest.TestCase):
    def test_largest_remainder_and_stable_ties(self):
        self.assertEqual(allocate(10, [1, 1, 1]), [4, 3, 3])
        self.assertEqual(allocate(5, [0, 2, 1]), [0, 3, 2])

    def test_decimal_weights_and_large_integer_exactness(self):
        result = allocate(10**18 + 7, [Decimal("0.1"), Decimal("0.2"), Decimal("0.7")])
        self.assertEqual(sum(result), 10**18 + 7)
        self.assertTrue(all(isinstance(value, int) for value in result))
        self.assertEqual(result, [100000000000000001, 200000000000000001, 700000000000000005])

    def test_invalid_inputs(self):
        for total, weights in [(-1, [1]), (1, []), (1, [0, 0]), (1, [1, -1]), (1, [float("inf")]), (1, [float("nan")])]:
            with self.subTest(total=total, weights=weights):
                with self.assertRaises(ValueError): allocate(total, weights)


if __name__ == "__main__": unittest.main()
