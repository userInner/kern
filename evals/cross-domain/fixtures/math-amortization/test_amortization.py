from decimal import Decimal
import unittest

from amortization import schedule


class AmortizationTests(unittest.TestCase):
    def assert_reconciles(self, principal, rows):
        balance = principal
        principal_paid = 0
        for row in rows:
            self.assertEqual(row["payment_cents"], row["principal_cents"] + row["interest_cents"])
            balance -= row["principal_cents"]
            self.assertEqual(row["balance_cents"], balance)
            self.assertGreaterEqual(row["principal_cents"], 0)
            self.assertGreaterEqual(row["interest_cents"], 0)
            principal_paid += row["principal_cents"]
        self.assertEqual(principal_paid, principal)
        self.assertEqual(balance, 0)

    def test_zero_rate_distributes_remainder_to_last_payment(self):
        rows = schedule(10000, Decimal("0"), 3)
        self.assertEqual([row["payment_cents"] for row in rows], [3333, 3333, 3334])
        self.assertEqual([row["interest_cents"] for row in rows], [0, 0, 0])
        self.assert_reconciles(10000, rows)

    def test_positive_rate_is_cent_exact_and_half_up(self):
        rows = schedule(120000, Decimal("0.12"), 12)
        self.assertEqual(len(rows), 12)
        self.assertEqual(rows[0]["interest_cents"], 1200)
        self.assert_reconciles(120000, rows)
        self.assertLessEqual(abs(rows[-1]["payment_cents"] - rows[-2]["payment_cents"]), 2)

    def test_invalid_inputs(self):
        for principal, rate, months in [(0, Decimal("0.1"), 12), (-1, Decimal("0.1"), 12), (1, Decimal("-0.1"), 12), (1, Decimal("0.1"), 0)]:
            with self.subTest(principal=principal, rate=rate, months=months):
                with self.assertRaises(ValueError): schedule(principal, rate, months)


if __name__ == "__main__": unittest.main()
