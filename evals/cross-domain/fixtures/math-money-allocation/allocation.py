def allocate(total_cents, weights):
    total_weight = sum(weights)
    return [round(total_cents * weight / total_weight) for weight in weights]
