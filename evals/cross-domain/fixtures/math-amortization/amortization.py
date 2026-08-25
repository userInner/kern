from decimal import Decimal


def schedule(principal_cents, annual_rate, months):
    rate = float(annual_rate) / 12
    payment = round(principal_cents / months if rate == 0 else principal_cents * rate / (1 - (1 + rate) ** -months))
    balance = principal_cents
    rows = []
    for _ in range(months):
        interest = round(balance * rate)
        principal = payment - interest
        balance -= principal
        rows.append({"payment_cents": payment, "principal_cents": principal, "interest_cents": interest, "balance_cents": balance})
    return rows
